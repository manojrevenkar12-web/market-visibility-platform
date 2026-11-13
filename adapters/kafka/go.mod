module github.com/manojrevenkar12-web/market-visibility-platform/adapters/kafka

go 1.24

require (
	github.com/manojrevenkar12-web/market-visibility-platform v0.0.0
	github.com/twmb/franz-go v1.18.1
	github.com/twmb/franz-go/pkg/kadm v1.14.0
)

replace github.com/manojrevenkar12-web/market-visibility-platform => ../..
