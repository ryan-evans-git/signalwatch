package kafka

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/aws/aws-msk-iam-sasl-signer-go/signer"
	kg "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"
)

// MSKIAMMechanismName is the SASL mechanism string the AWS MSK
// brokers expect.
const MSKIAMMechanismName = "AWS_MSK_IAM"

// tokenProvider returns an IAM-signed SASL token for the given AWS
// region. Pluggable so tests can inject a deterministic provider
// without hitting the AWS metadata endpoint. The default uses the
// AWS SDK's default credential chain.
type tokenProvider func(ctx context.Context, region string) (string, error)

// defaultTokenProvider calls aws-msk-iam-sasl-signer-go to produce
// an IAM-signed presigned URL token. The SDK's default credential
// chain is used (env / shared config / IMDS / IRSA).
func defaultTokenProvider(ctx context.Context, region string) (string, error) {
	token, _, err := signer.GenerateAuthToken(ctx, region)
	return token, err
}

// mskIAMMechanism implements segmentio/kafka-go's sasl.Mechanism
// over the MSK IAM auth-token protocol.
//
// The exchange is a single round trip: the client sends the signed
// token in the initial SASL response, the broker validates against
// IAM, and the session completes.
type mskIAMMechanism struct {
	region   string
	provider tokenProvider
}

func newMSKIAMMechanism(region string, provider tokenProvider) *mskIAMMechanism {
	if provider == nil {
		provider = defaultTokenProvider
	}
	return &mskIAMMechanism{region: region, provider: provider}
}

func (*mskIAMMechanism) Name() string { return MSKIAMMechanismName }

func (m *mskIAMMechanism) Start(ctx context.Context) (sasl.StateMachine, []byte, error) {
	token, err := m.provider(ctx, m.region)
	if err != nil {
		return nil, nil, err
	}
	return &mskIAMState{}, []byte(token), nil
}

// mskIAMState terminates immediately on the first Next() because the
// MSK IAM exchange is one round trip.
type mskIAMState struct{}

func (*mskIAMState) Next(_ context.Context, _ []byte) (done bool, response []byte, err error) {
	return true, nil, nil
}

// buildMSKDialer returns a kafka-go Dialer wired with the IAM SASL
// mechanism + TLS — both required by MSK.
func buildMSKDialer(region string, provider tokenProvider, dialTimeout time.Duration) *kg.Dialer {
	return &kg.Dialer{
		Timeout:       dialTimeout,
		DualStack:     true,
		SASLMechanism: newMSKIAMMechanism(region, provider),
		TLS:           &tls.Config{MinVersion: tls.VersionTLS12},
	}
}
