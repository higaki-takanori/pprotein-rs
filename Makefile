.PHONY: noop
noop:

.PHONY: run
run:
	go run ./cli/pprotein

.PHONY: run-agent
run-agent:
	go run ./cli/pprotein-agent

.PHONY: build
build: pprotein pprotein-agent

# 出力ファイル名と同じターゲットだが、ソース変更を反映するため毎回ビルドする（Go のキャッシュで差分のみ）
.PHONY: pprotein pprotein-agent view/dist

pprotein: view/dist
	go build -trimpath -ldflags="-w -s" ./cli/pprotein

pprotein-agent:
	go build -trimpath -ldflags="-w -s" ./cli/pprotein-agent

# 計測対象サーバー（Linux）向けの pprotein-agent。ARM なら make agent-linux GOARCH=arm64
GOARCH ?= amd64

.PHONY: agent-linux
agent-linux:
	GOOS=linux GOARCH=$(GOARCH) CGO_ENABLED=0 go build -trimpath -ldflags="-w -s" -o pprotein-agent-linux-$(GOARCH) ./cli/pprotein-agent

view/dist: view/node_modules
	npm --prefix view run build

view/node_modules: view/package-lock.json
	npm --prefix view ci

.PHONY: clean
clean:
	rm -rf pprotein pprotein-agent pprotein-agent-linux-* view/dist
