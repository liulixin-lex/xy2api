//go:build unit

package server_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type chainOps struct{ service.OpsRepository }

func (*chainOps) GetErrorLogByID(context.Context, int64) (*service.OpsErrorLogDetail, error) {
	return &service.OpsErrorLogDetail{
		OpsErrorLog:    service.OpsErrorLog{ID: 10},
		UpstreamErrors: `[{"upstream_url":"https://alice:password@203.0.113.10/v1","message":"upstream failure at https://alice:password@203.0.113.10/v1"}]`,
	}, nil
}

func TestOperatorOpsDetailsAlwaysStripURLUserinfo(t *testing.T) {
	for _, path := range []string{"/ops/errors/10", "/ops/upstream-errors/10"} {
		for _, withFlag := range []bool{false, true} {
			name := path + "/without_flag"
			var flags []operatoraccess.Flag
			if withFlag {
				name = path + "/with_flag"
				flags = []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}
			}
			t.Run(name, func(t *testing.T) {
				f := newOperatorChain(t, service.RoleOperator, false, flags, false)
				perms := operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleOps: operatoraccess.LevelView}, Flags: map[operatoraccess.Flag]bool{operatoraccess.FlagAccountsUpstreamAddress: withFlag}}
				var err error
				f.permissions.raw, err = json.Marshal(perms.Wire())
				require.NoError(t, err)
				rec := f.do("GET", path, "")
				require.Equal(t, 200, rec.Code, rec.Body.String())
				require.NotContains(t, rec.Body.String(), "alice")
				require.NotContains(t, rec.Body.String(), "password")
				var response struct {
					Data struct {
						UpstreamErrors string `json:"upstream_errors"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
				var upstream []map[string]any
				require.NoError(t, json.Unmarshal([]byte(response.Data.UpstreamErrors), &upstream))
				require.Len(t, upstream, 1)
				require.Equal(t, "https://***@203.0.113.10/v1", upstream[0]["upstream_url"])
				require.Contains(t, upstream[0]["message"], "upstream failure", "ops:view retains error text and URLs")
			})
		}
	}
}
