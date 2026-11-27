package usageexport

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestProjectionCompatibility(t *testing.T) {
	var r Record
	require.NoError(t, json.Unmarshal([]byte(`{"created_at":"2026-09-22T00:00:00Z","model":"upstream","requested_model":"client","requested_reasoning_effort":"HIGH","reasoning_effort":"max","image_count":1,"actual_cost":0.12345678,"total_cost":0.23456789,"account_stats_cost":0.3,"account_rate_multiplier":0.5,"input_tokens":9007199254740993,"request_type":2,"key_name":"=secret","user_email":"admin-only@example.test"}`), &r))
	o := options()
	o.Timezone = "Asia/Shanghai"
	o.Language = "zh"
	row, err := Project(r, o)
	require.NoError(t, err)
	require.Len(t, row, 17)
	require.Contains(t, row[0], "08:00:00+08:00")
	require.Equal(t, "client", row[2])
	require.Equal(t, "High", row[3])
	require.Equal(t, "Stream", row[6])
	require.Equal(t, "按次(图片)", row[7])
	require.Equal(t, "9007199254740993", row[8])
	require.Equal(t, "0.12345678", row[13])
	require.NotContains(t, row, "admin-only@example.test")
	r["billing_mode"] = json.RawMessage(`"token"`)
	row, err = Project(r, o)
	require.NoError(t, err)
	require.Equal(t, "按量", row[7])
	r["billing_mode"] = json.RawMessage(`"video"`)
	row, err = Project(r, o)
	require.NoError(t, err)
	require.Equal(t, "按次(视频)", row[7])
	o.Format = "xlsx"
	row, err = Project(r, o)
	require.NoError(t, err)
	require.Len(t, row, 33)
	require.Equal(t, "admin-only@example.test", row[1])
	require.Equal(t, "High", row[8])
	require.Equal(t, "Max", row[9])
	require.Equal(t, "0.150000", row[26])
}

func TestProjectServiceTierEvidenceVisibility(t *testing.T) {
	var r Record
	require.NoError(t, json.Unmarshal([]byte(`{"created_at":"2026-09-22T00:00:00Z","service_tier":"default","service_tier_details":{"version":1,"settled_tier":"default","cap_reason":"outbound_price","serving_group_id":12,"reason":"group_rule"}}`), &r))
	o := options()
	o.Version = ServiceTierFormatVersion
	row, err := Project(r, o)
	require.NoError(t, err)
	require.Len(t, row, len(Headers(o)))
	require.Equal(t, "default", row[17])
	require.JSONEq(t, `{"version":1,"settled_tier":"default","cap_reason":"outbound_price"}`, row[18])
	o.Format = "xlsx"
	row, err = Project(r, o)
	require.NoError(t, err)
	require.NotContains(t, row[34], "serving_group_id")
	o.IncludeInternalTierDetails = true
	row, err = Project(r, o)
	require.NoError(t, err)
	require.Contains(t, row[34], "serving_group_id")
}

func TestMeteringProjectionDoesNotMultiplySupplierTwice(t *testing.T) {
	var r Record
	require.NoError(t, json.Unmarshal([]byte(`{"created_at":"2026-01-01T00:00:00Z","total_cost":2,"account_stats_cost":1,"account_rate_multiplier":0.5,"supplier_cost_estimate":0.9,"metering_adjustment":{"v":1}}`), &r))
	o := options()
	o.Format, o.Version = "xlsx", MeteringFormatVersion
	row, err := Project(r, o)
	require.NoError(t, err)
	require.Len(t, row, len(Headers(o)))
	require.Equal(t, "0.900000", row[26])
	require.Equal(t, "0.9", row[35])
	require.JSONEq(t, `{"v":1}`, row[36])
	r["supplier_cost_estimate"] = json.RawMessage("0")
	row, err = Project(r, o)
	require.NoError(t, err)
	require.Equal(t, "0.000000", row[26])
	for _, version := range []int{1, ServiceTierFormatVersion} {
		o.Version = version
		row, err = Project(r, o)
		require.NoError(t, err)
		require.Equal(t, "0.500000", row[26])
	}
	o.Format, o.Version = "csv", MeteringFormatVersion
	row, err = Project(r, o)
	require.NoError(t, err)
	require.Len(t, row, 19)
	require.NotContains(t, row, `{"v":1}`)
}
