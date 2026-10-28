module github.com/jackielii/structpages/examples/url-validation

go 1.25.0

require github.com/jackielii/structpages v0.0.0-00010101000000-000000000000

require (
	github.com/syumai/workers-go v0.36.0 // indirect
	golang.org/x/mod v0.33.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/tools v0.42.0 // indirect
)

require (
	github.com/jackielii/ctxkey v1.0.1 // indirect
	github.com/jackielii/structpages/tools/static v0.0.0 // indirect
	github.com/jackielii/structpages/tools/workers v0.0.0 // indirect
	github.com/jackielii/structpages/workers v0.0.0
)

replace github.com/jackielii/structpages => ../..

replace github.com/jackielii/structpages/workers => ../../workers

replace github.com/jackielii/structpages/tools/static => ../../tools/static

tool (
	github.com/jackielii/structpages/tools/static/cmd/structpages-static
	github.com/jackielii/structpages/tools/workers/cmd/structpages-workers
)

replace github.com/jackielii/structpages/tools/workers => ../../tools/workers
