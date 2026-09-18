package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/payment"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestStripeModeSelection(t *testing.T) {
	for _, tc := range []struct{ stored, mode string }{
		{"alipay,stripe", payment.TypeStripe},
		{"stripe_hosted,wxpay", payment.TypeStripeHosted},
		{"stripe,stripe_hosted", payment.TypeStripeHosted},
		{"wxpay", ""},
	} {
		t.Run(tc.stored, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			for _, kind := range []string{payment.TypeStripe, payment.TypeStripeHosted} {
				_, err := client.PaymentProviderInstance.Create().SetProviderKey(kind).SetName(kind).SetConfig(`{"currency":"CNY"}`).SetSupportedTypes(kind).SetEnabled(true).Save(ctx)
				require.NoError(t, err)
			}
			svc := &PaymentConfigService{entClient: client, settingRepo: &paymentConfigSettingRepoStub{values: map[string]string{SettingEnabledPaymentTypes: tc.stored}}}
			cfg, err := svc.GetPaymentConfig(ctx)
			require.NoError(t, err)
			require.Equal(t, tc.mode, StripePaymentMode(cfg.EnabledTypes))
			limits, err := svc.GetAvailableMethodLimits(ctx)
			require.NoError(t, err)
			if tc.mode == "" {
				require.Empty(t, limits.Methods)
			} else {
				require.Len(t, limits.Methods, 1)
				require.Contains(t, limits.Methods, tc.mode)
			}
			// Mode selection must leave historical provider records intact.
			count, err := client.PaymentProviderInstance.Query().Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 2, count)
		})
	}
}

func TestStripeModeRejectsConflictingUpdate(t *testing.T) {
	svc := &PaymentConfigService{}
	err := svc.UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{EnabledTypes: []string{"stripe", "stripe_hosted"}})
	require.Equal(t, "STRIPE_MODE_CONFLICT", infraerrors.FromError(err).Reason)
}

func TestStripeModeRejectsNewEmbeddedOrderWhenHostedSelected(t *testing.T) {
	svc := &PaymentService{configService: &PaymentConfigService{settingRepo: &paymentConfigSettingRepoStub{values: map[string]string{SettingPaymentEnabled: "true", SettingEnabledPaymentTypes: "stripe_hosted"}}}}
	_, err := svc.CreateOrder(context.Background(), CreateOrderRequest{PaymentType: "stripe", Amount: 10})
	require.Equal(t, "PAYMENT_TYPE_DISABLED", infraerrors.FromError(err).Reason)
}
