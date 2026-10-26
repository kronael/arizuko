module github.com/kronael/arizuko

go 1.27

toolchain go1.27rc2

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/aws/aws-sdk-go-v2 v1.47.0
	github.com/aws/aws-sdk-go-v2/credentials v1.20.4
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.68.0
	github.com/aws/smithy-go v1.28.1
	github.com/bwmarrin/discordgo v0.29.0
	github.com/coreos/go-oidc/v3 v3.18.0
	github.com/emersion/go-imap/v2 v2.0.0-beta.8
	github.com/emersion/go-message v0.18.2
	github.com/emersion/go-msgauth v0.7.0
	github.com/go-jose/go-jose/v4 v4.1.4
	github.com/google/uuid v1.6.0
	github.com/joho/godotenv v1.5.1
	github.com/mark3labs/mcp-go v0.45.0
	github.com/matterbridge/telegram-bot-api/v6 v6.5.0
	github.com/pelletier/go-toml/v2 v2.4.3
	github.com/prometheus/client_golang v1.23.2
	github.com/robfig/cron/v3 v3.0.1
	go.opentelemetry.io/contrib/bridges/otelslog v0.18.0
	go.opentelemetry.io/otel v1.43.0
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.19.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.43.0
	go.opentelemetry.io/otel/sdk v1.43.0
	go.opentelemetry.io/otel/sdk/log v0.19.0
	go.opentelemetry.io/otel/trace v1.43.0
	golang.org/x/net v0.57.0
	golang.org/x/oauth2 v0.36.0
	golang.org/x/sys v0.47.0
	golang.org/x/text v0.40.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.3 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.13.3 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/buger/jsonparser v1.1.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/emersion/go-sasl v0.0.0-20241020182733-b788ff22d5a6 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.28.0 // indirect
	github.com/invopop/jsonschema v0.13.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.43.0 // indirect
	go.opentelemetry.io/otel/log v0.19.0 // indirect
	go.opentelemetry.io/otel/metric v1.43.0 // indirect
	go.opentelemetry.io/proto/otlp v1.10.0 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260401024825-9d38bb4040a9 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260401024825-9d38bb4040a9 // indirect
	google.golang.org/grpc v1.80.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/bwmarrin/discordgo => ./third_party/discordgo
