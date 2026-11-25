package service

// 国产供应商 Anthropic 协议转换路径的上游 SSE 行泵。
//
// 这两条转换链（CC×anthropic / Responses×anthropic）的上游 ctx 是
// WithoutCancel（detachStreamUpstreamContext）、http.Client 无整体 Timeout，
// 客户端断开后的排水阶段若上游挂住 SSE（不发数据也不断连），
// scanner.Scan() 将永久阻塞：goroutine 钉死、resp.Body 不归还、连接池位
// 被占用、usage 永不落库。
//
// 与 handleAnthropicStreamingResponse / readOpenAICompatBufferedTerminal 的
// 同类排水一致，本泵用 gateway.stream_data_interval_timeout（默认 180s）作为
// 业务进展间隔上限，注释与 ping 不续期；超时返回 errAnthropicNativeStreamIdle，由调用方
// 关闭 resp.Body 解除阻塞的读并结束排水。

import (
	"bufio"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// errAnthropicNativeStreamIdle 表示上游流读间隔超时（见上方文件注释）。
var errAnthropicNativeStreamIdle = errors.New("stream data interval timeout")

// anthropicNativeLineEvent 是行泵交付的单次读取结果：line 为一行 SSE 文本，
// err 为 scanner 读错误（流自然结束时 next 返回 io.EOF，不经过本字段）。
type anthropicNativeLineEvent struct {
	line string
	err  error
}

// anthropicNativeLinePump 以独立 goroutine 泵送原始行，对完整业务帧的
// 到达间隔应用既有读取/停滞期限；首输出 guard 生效时不提前截断。
type anthropicNativeLinePump struct {
	events        chan anthropicNativeLineEvent
	done          chan struct{}
	timer         *time.Timer
	untimed       bool
	interval      time.Duration
	progress      anthropicNativeProgress
	firstEvent    *openAICompatFirstEvent
	outputStarted func() bool
}

// The reader owns the parser; deadlines read the arrival timestamps atomically.
// Consuming buffered lines must never make old traffic look like new progress.
type anthropicNativeProgress struct {
	parser         openAICompatSSEFrameParser
	lastReadAt     atomic.Int64
	lastProgressAt atomic.Int64
}

func (p *anthropicNativeProgress) start() {
	now := time.Now().UnixNano()
	p.lastReadAt.Store(now)
	p.lastProgressAt.Store(now)
}

func (p *anthropicNativeProgress) observeLine(line string) {
	now := time.Now().UnixNano()
	p.lastReadAt.Store(now)
	frame, complete := p.parser.AddLine(line)
	if !complete || !gjson.Valid(frame.Data) || !gjson.Parse(frame.Data).IsObject() {
		return
	}
	if effectiveOpenAISSEEventType([]byte(frame.Data), frame.EventType) == "ping" {
		return
	}
	if resilience.ClassifyStreamEvent("messages", frame.EventType, []byte(frame.Data)).Kind != resilience.Unknown {
		p.lastProgressAt.Store(now)
	}
}

// newAnthropicNativeLinePump 启动泵 goroutine；调用方 defer pump.stop()。
func newAnthropicNativeLinePump(scanner *bufio.Scanner, interval time.Duration) *anthropicNativeLinePump {
	p := &anthropicNativeLinePump{
		events:   make(chan anthropicNativeLineEvent, 16),
		done:     make(chan struct{}),
		interval: interval,
	}
	p.progress.start()
	if interval > 0 {
		p.timer = time.NewTimer(interval)
	}
	go func() {
		defer close(p.events)
		for scanner.Scan() {
			p.progress.observeLine(scanner.Text())
			select {
			case p.events <- anthropicNativeLineEvent{line: scanner.Text()}:
			case <-p.done:
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case p.events <- anthropicNativeLineEvent{err: err}:
			case <-p.done:
			}
		}
	}()
	return p
}

// next 阻塞返回下一行。返回 io.EOF 表示上游正常收流；errAnthropicNativeStreamIdle
// 表示期限内没有完整业务帧到达。期限从读取时起算，包含排队和事件处理耗时。
func (p *anthropicNativeLinePump) next() (string, error) {
	for {
		remaining, bounded := p.remaining()
		if bounded && remaining <= 0 {
			return "", errAnthropicNativeStreamIdle
		}
		var timeoutCh <-chan time.Time
		if bounded {
			p.resetTimer(remaining)
			timeoutCh = p.timer.C
		}
		select {
		case ev, ok := <-p.events:
			if remaining, bounded := p.remaining(); bounded && remaining <= 0 {
				return "", errAnthropicNativeStreamIdle
			}
			if !ok {
				return "", io.EOF
			}
			return ev.line, ev.err
		case <-timeoutCh:
			// A business frame may have advanced the deadline while next waited.
		}
	}
}

func (p *anthropicNativeLinePump) remaining() (time.Duration, bool) {
	if p.untimed {
		return 0, false
	}
	started := p.outputStarted != nil && p.outputStarted()
	if !started && p.firstEvent != nil && !p.firstEvent.disarmed.Load() {
		return 0, false
	}
	interval := resilienceStallThreshold(p.interval, started)
	if interval <= 0 {
		return 0, false
	}
	return time.Until(time.Unix(0, p.progress.lastProgressAt.Load()).Add(interval)), true
}

func (p *anthropicNativeLinePump) resetTimer(remaining time.Duration) {
	if p.timer == nil {
		p.timer = time.NewTimer(remaining)
		return
	}
	if !p.timer.Stop() {
		select {
		case <-p.timer.C:
		default:
		}
	}
	p.timer.Reset(remaining)
}

// stop 终止泵 goroutine。注意：goroutine 若正阻塞在 scanner.Read 上，需由
// 调用方关闭 resp.Body（间隔超时分支已做）才能真正退出。
func (p *anthropicNativeLinePump) stop() {
	close(p.done)
	if p.timer != nil {
		if !p.timer.Stop() {
			select {
			case <-p.timer.C:
			default:
			}
		}
	}
}

// anthropicNativeStreamInterval 返回本组转换路径适用的读间隔上限；
// gateway.stream_data_interval_timeout <= 0 时视为禁用。
func (s *OpenAIGatewayService) anthropicNativeStreamInterval() time.Duration {
	if s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		return time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	return 0
}
