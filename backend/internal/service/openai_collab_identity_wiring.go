package service

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// PrepareOpenAICollabIdentity binds optional records to authenticated tenant and
// group scope. Contradictory thread facts omit evidence instead of inventing a
// root or falling back to an unkeyed identity.
func (s *OpenAIGatewayService) PrepareOpenAICollabIdentity(c *gin.Context) {
	defer func() { _ = recover() }()
	r := openAICollabRequestFrom(c.Request.Context())
	if r == nil || r.gateway != nil {
		return
	}
	r.gateway = s
	metadata := c.GetHeader(codexTurnMetadataHeader)
	if metadata != "" && !gjson.Valid(metadata) {
		return
	}
	thread := strings.TrimSpace(gjson.Get(metadata, "thread_id").String())
	explicit := strings.TrimSpace(explicitOpenAIRequestSessionID(c, r.state.Original))
	if thread != "" && explicit != "" && thread != explicit {
		return
	}
	if thread == "" {
		thread = explicit
	}
	if thread == "" {
		return
	}
	r.thread = thread
	parent := strings.TrimSpace(gjson.Get(metadata, "parent_thread_id").String())
	if header := strings.TrimSpace(c.GetHeader(codexParentThreadIDHeader)); header != "" {
		if parent != "" && parent != header {
			return
		}
		parent = header
	}
	if parent == thread {
		return
	}
	tree := strings.TrimSpace(gjson.Get(metadata, "root_thread_id").String())
	if tree == "" {
		tree = strings.TrimSpace(gjson.Get(metadata, "tree_id").String())
	}
	role := resilience.CollabRoleRoot
	if parent != "" {
		role = resilience.CollabRoleChild
	}
	if tree == "" && role == resilience.CollabRoleRoot {
		tree = thread
	}
	tenant := s.CollabDigest(resilience.CollabDigestDomainScope, "tenant-v1", strconv.FormatInt(r.state.UserID, 10))
	group := s.CollabDigest(resilience.CollabDigestDomainScope, "group-v1", strconv.FormatInt(r.state.GroupID, 10))
	r.scope = resilience.CollabRecordScope{Tenant: tenant, Group: group}
	r.state.Thread = s.CollabDigest(resilience.CollabDigestDomainThread, thread)
	if parent != "" {
		r.state.Parent = s.CollabDigest(resilience.CollabDigestDomainThread, parent)
	}
	if tree != "" {
		r.scope.Tree = s.CollabDigest(resilience.CollabDigestDomainTree, tree)
		r.state.Tree = r.scope.Tree
	}
	r.state.Role = role
	// Identity is known before any send/observation can be sealed.
	r.initObservation(c)
	r.initCipherItems()
	r.initCollabAuxiliaryItems()
}

func (r *openAICollabRequest) initCipherItems() {
	if r.gateway == nil || r.scope.Tree == "" || r.state.Thread == "" {
		return
	}
	parsed := ParseCollabBodyWithContext(r.state.Original, CollabBodyContext{Thread: r.thread, ClientVersion: r.version})
	for _, item := range parsed.Items {
		if !collabRecordItemIdentity(item.Identity) {
			continue
		}
		if item.Family != resilience.CollabFamilyAgentMessage && item.Family != resilience.CollabFamilyToolOutput {
			continue
		}
		for index, path := range item.Paths {
			value := gjson.GetBytes(r.state.Original, path)
			if value.IsObject() {
				value = value.Get("encrypted_content")
			}
			if value.Type != gjson.String {
				continue
			}
			digest := r.gateway.CollabDigest(resilience.CollabDigestDomainCiphertext, value.String())
			itemDigest := r.gateway.CollabDigest(resilience.CollabDigestDomainItem, item.Identity, strconv.Itoa(index))
			key := resilience.CollabDeliveryKey{Scope: r.scope, Thread: r.state.Thread, Item: itemDigest, Digest: digest, Delivery: r.gateway.CollabDigest(resilience.CollabDigestDomainDelivery, string(r.state.Thread), item.Identity, strconv.Itoa(index))}
			kind := resilience.CollabMessageKindMessage
			if item.Class == resilience.CollabItemClassProtected {
				kind = resilience.CollabMessageKindTask
			}
			if item.Family == resilience.CollabFamilyToolOutput {
				kind = resilience.CollabMessageKindToolOutput
			}
			r.state.Items = append(r.state.Items, resilience.CollabCipherItem{Key: key, Class: item.Class, Kind: kind, Family: item.Family, Position: resilience.CollabInputPosition{InputIndex: item.Index, FragmentIndex: index, Position: resilience.CollabPositionMessage}})
			r.itemPaths = append(r.itemPaths, path)
		}
	}
}
