# FreeLLM build targets
#   make          - build for current OS
#   make linux    - cross-compile for Linux amd64
#   make windows  - build for Windows amd64
#   make darwin   - cross-compile for macOS amd64
#   make all      - build all platforms
#   make clean    - remove build artifacts

VERSION := $(shell cat VERSION.md 2>/dev/null || echo "dev")
GOFLAGS := -buildvcs=false -ldflags="-X main.version=$(VERSION)"
OUTDIR := build

.PHONY: all linux windows darwin clean deploy deploy-hetzner

default: $(shell go env GOOS)

linux:
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o $(OUTDIR)/freellm-linux-$(VERSION) ./cmd/app/
	ln -sf freellm-linux-$(VERSION) $(OUTDIR)/freellm-linux
	@echo "Built: $(OUTDIR)/freellm-linux-$(VERSION)"

windows:
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -o $(OUTDIR)/freellm-$(VERSION).exe ./cmd/app/
	@echo "Built: $(OUTDIR)/freellm-$(VERSION).exe"

darwin:
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -o $(OUTDIR)/freellm-darwin-$(VERSION) ./cmd/app/
	@echo "Built: $(OUTDIR)/freellm-darwin-$(VERSION)"

all: linux windows darwin

clean:
	rm -rf $(OUTDIR)
	rm -f freellm-linux freellm.exe freellm.exe~

deploy: linux
	@echo "To deploy: make deploy-hetzner"

deploy-hetzner:
	scp $(OUTDIR)/freellm-linux hetzner:/tmp/freellm-linux-new
	ssh hetzner "\
		systemctl stop aimm-freellm; \
		cp /opt/aimoneymachine/freellm /opt/aimoneymachine/freellm.bak.$$(date +%Y%m%d-%H%M%S); \
		mv /tmp/freellm-linux-new /opt/aimoneymachine/freellm; \
		chmod +x /opt/aimoneymachine/freellm; \
		systemctl start aimm-freellm; \
		systemctl status aimm-freellm --no-pager -l | head -10; \
	"
