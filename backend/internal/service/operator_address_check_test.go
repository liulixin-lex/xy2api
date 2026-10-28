//go:build unit

package service

import (
	"context"
	"net"
	"testing"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

func operatorTestDNS(t *testing.T) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			var p dnsmessage.Parser
			header, err := p.Start(buf[:n])
			if err != nil {
				continue
			}
			q, err := p.Question()
			if err != nil {
				continue
			}
			answer := dnsmessage.Header{ID: header.ID, Response: true, RecursionDesired: true, RecursionAvailable: true}
			switch q.Name.String() {
			case "public.operator.test.", "private.operator.test.", "link.operator.test.", "empty.operator.test.":
			default:
				answer.RCode = dnsmessage.RCodeNameError
			}
			b := dnsmessage.NewBuilder(nil, answer)
			_ = b.StartQuestions()
			_ = b.Question(q)
			_ = b.StartAnswers()
			if q.Type == dnsmessage.TypeA && answer.RCode == dnsmessage.RCodeSuccess && q.Name.String() != "empty.operator.test." {
				ip := [4]byte{203, 0, 113, 10}
				switch q.Name.String() {
				case "private.operator.test.":
					ip = [4]byte{10, 1, 2, 3}
				case "link.operator.test.":
					ip = [4]byte{169, 254, 1, 1}
				}
				_ = b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1}, dnsmessage.AResource{A: ip})
			}
			raw, err := b.Finish()
			if err == nil {
				_, _ = conn.WriteTo(raw, addr)
			}
		}
	}()
	original := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", conn.LocalAddr().String())
	}}
	t.Cleanup(func() { net.DefaultResolver = original; _ = conn.Close(); <-done })
}
func TestOperatorAddressCheck(t *testing.T) {
	operatorTestDNS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, raw := range []string{"https://203.0.113.10/x", "http://public.operator.test/x"} {
		require.NoError(t, validateOperatorAddress(ctx, raw), raw)
	}
	for _, raw := range []string{"ftp://203.0.113.10/x", "not-a-url", "https://127.0.0.1", "https://10.1.2.3", "https://172.16.1.1", "https://192.168.1.1", "https://100.64.1.1", "https://169.254.169.254", "https://[::1]", "https://[fc00::1]", "https://[fe80::1]", "https://metadata.google.internal", "https://metadata.google.internal.", "https://private.operator.test", "https://link.operator.test", "https://empty.operator.test", "https://failure.operator.test"} {
		err := validateOperatorAddress(ctx, raw)
		require.Error(t, err, raw)
		guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "")
		require.Equal(t, "private_address", infraerrors.FromError(err).Metadata["cause"], raw)
	}
	for _, host := range []string{"public.operator.test", "203.0.113.10"} {
		require.NoError(t, validateOperatorHost(ctx, host))
	}
}
