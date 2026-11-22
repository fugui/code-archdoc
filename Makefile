BINARY          := code-archdoc-server
FRONTEND_DIR    := frontend
DIST_DIR        := $(FRONTEND_DIR)/dist
NODE_MODULES    := $(FRONTEND_DIR)/node_modules

VERSION         ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT          ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILDTIME       ?= $(shell date -u '+%Y-%m-%d %H:%M:%S')
LDFLAGS         := -X 'main.Version=$(VERSION)' -X 'main.CommitID=$(COMMIT)' -X 'main.BuildTime=$(BUILDTIME)' -X 'code-archdoc/config.AppVersion=$(VERSION)'

COMMON_DIR      := ../code-common/frontend/src
COMMON_SRCS     := $(shell find $(COMMON_DIR) -type f 2>/dev/null)

# 自动收集前端与后端源码依赖
FRONTEND_SRCS   := $(shell find $(FRONTEND_DIR) -type f -not -path "*/node_modules/*" -not -path "*/dist/*" 2>/dev/null) $(COMMON_SRCS)
BACKEND_SRCS    := $(shell find . -type f \( -name "*.go" -o -name "go.mod" -o -name "go.sum" \) -not -path "*/$(FRONTEND_DIR)/*" -not -path "*/.git/*")

.PHONY: all build install frontend backend clean run test lint lint-arch check-cgo eval-baseline

# 默认运行目标
all: build

# 校验 CGO 编译环境（tree-sitter C/C++/Java/Python 解析器需要 CGO_ENABLED=1 与本机 C 编译器）
check-cgo:
	@if [ "$$(go env CGO_ENABLED)" != "1" ]; then \
		echo "❌ [CGO Check Failed] 需要 CGO_ENABLED=1，当前为 $$(go env CGO_ENABLED)"; \
		exit 1; \
	fi
	@if ! command -v gcc >/dev/null 2>&1 && ! command -v cc >/dev/null 2>&1; then \
		echo "❌ [CGO Check Failed] 未检测到 gcc 或 cc 编译器"; \
		exit 1; \
	fi
	@echo "✅ [CGO Check Passed] CGO_ENABLED=1 且 C 编译器就绪。"

# 架构防腐静态门禁 (ArchGuard 5 条红线)
lint-arch:
	@echo "Checking architecture boundaries (ArchGuard)..."
	@clean_dist=0; \
	if [ ! -d "$(DIST_DIR)" ]; then \
		mkdir -p "$(DIST_DIR)" && touch "$(DIST_DIR)/.embed_placeholder"; \
		clean_dist=1; \
	fi; \
	cleanup() { if [ "$$clean_dist" = "1" ]; then rm -rf "$(DIST_DIR)"; fi; }; \
	trap cleanup EXIT; \
	if [ -d "pkg" ] && [ -n "$$(find pkg -name '*.go' -print -quit)" ]; then \
		bad_pkg_imports=$$(go list -f '{{.ImportPath}} -> {{.Imports}}' ./pkg/... | grep -E 'code-archdoc/models|code-archdoc/services|code-archdoc/handlers|gorm\.io/gorm|github\.com/gin-gonic/gin' || true); \
		if [ -n "$$bad_pkg_imports" ]; then \
			echo "❌ [ArchGuard Failed] pkg/* must not import models/services/handlers/gorm/gin:"; \
			echo "$$bad_pkg_imports"; exit 1; \
		fi; \
	fi; \
	search_dirs=""; \
	[ -d "pkg" ] && search_dirs="$$search_dirs pkg/"; \
	[ -d "services" ] && search_dirs="$$search_dirs services/"; \
	if [ -n "$$search_dirs" ]; then \
		if grep -rn "models\.DB" $$search_dirs; then \
			echo "❌ [ArchGuard Failed] pkg/ and services/ must not access global models.DB directly!"; \
			exit 1; \
		fi; \
	fi; \
	pkgs=$$(go list ./... | grep -v '/$(FRONTEND_DIR)/'); \
	bad_cgo=$$(go list -f '{{.ImportPath}} -> {{.Imports}}' $$pkgs | grep -v '^code-archdoc/pkg/codeir/treesitter ' | grep 'tree-sitter' || true); \
	if [ -n "$$bad_cgo" ]; then \
		echo "❌ [ArchGuard Failed] tree-sitter CGO dependency must be isolated inside pkg/codeir/treesitter:"; \
		echo "$$bad_cgo"; exit 1; \
	fi; \
	bad_model_imports=$$(go list -f '{{.ImportPath}} -> {{.Imports}}' ./models/... | grep -E 'code-archdoc/(services|handlers)' || true); \
	if [ -n "$$bad_model_imports" ]; then \
		echo "❌ [ArchGuard Failed] models/ must not depend on services/ or handlers/:"; \
		echo "$$bad_model_imports"; exit 1; \
	fi; \
	bad_tables=$$(awk '/func \(.*\) TableName\(\) string/ { line=$$0; if (line !~ /return/) { getline nextline; line=line " " nextline } if (line !~ /ArchitectureElement/ && line !~ /config\.TablePrefix/ && line !~ /"archdoc_/) { print FILENAME ":" NR ":" line } }' models/*.go 2>/dev/null || true); \
	if [ -n "$$bad_tables" ]; then \
		echo "❌ [ArchGuard Failed] Own GORM models must return archdoc_ table prefix:"; \
		echo "$$bad_tables"; exit 1; \
	fi; \
	if [ -f "models/module.go" ] && grep -En 'OwnerID|owner_id' models/module.go >/dev/null 2>&1; then \
		echo "❌ [ArchGuard Failed] ADR-04: archdoc_repo_modules must not duplicate OwnerID (use architecture_elements):"; \
		grep -En 'OwnerID|owner_id' models/module.go; exit 1; \
	fi; \
	bad_gitsync=$$(grep -rn "gitsync\.Sync(" handlers/ pkg/ 2>/dev/null || true); \
	if [ -n "$$bad_gitsync" ]; then \
		echo "❌ [ArchGuard Failed] ADR-04: handlers/ and pkg/ must not call gitsync.Sync directly:"; \
		echo "$$bad_gitsync"; exit 1; \
	fi; \
	echo "✅ [ArchGuard Passed] All architectural boundary rules (incl. ADR-04) are clean."

# 完整打包构建（前端 dist + 内嵌单二进制）
build: check-cgo $(BINARY)

# 安装前端依赖
install: $(NODE_MODULES)

$(NODE_MODULES): $(FRONTEND_DIR)/package.json
	cd $(FRONTEND_DIR) && ( [ -d node_modules ] || npm install --no-package-lock )
	@touch $(NODE_MODULES)

# 编译构建前端静态资产 (frontend/dist/)
frontend: $(DIST_DIR)

$(DIST_DIR): $(NODE_MODULES) $(FRONTEND_SRCS)
	cd $(FRONTEND_DIR) && npm run build
	@touch $(DIST_DIR)

# 编译后端可执行文件
backend: check-cgo $(BINARY)

$(BINARY): $(BACKEND_SRCS) $(DIST_DIR)
	go mod tidy
	CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o $(BINARY)

# 清理构建产物
clean:
	rm -rf $(DIST_DIR) $(BINARY)

# 运行 Go 单元测试（支持在未构建 frontend/dist 前独立执行，自动过滤 node_modules 下的无关文件）
test:
	@clean_dist=0; \
	if [ ! -d "$(DIST_DIR)" ]; then \
		mkdir -p "$(DIST_DIR)" && touch "$(DIST_DIR)/.embed_placeholder"; \
		clean_dist=1; \
	fi; \
	cleanup() { if [ "$$clean_dist" = "1" ]; then rm -rf "$(DIST_DIR)"; fi; }; \
	trap cleanup EXIT; \
	pkgs=$$(go list ./... | grep -v '/$(FRONTEND_DIR)/'); \
	go fmt $$pkgs && go vet $$pkgs && go test -v $$pkgs

# 快捷启动命令
run: build
	./$(BINARY)

# 运行 F-26 Golden Sample 量化回归评测基线（零 LLM C/C++/Proto 分级准确率 + 0 密钥残留 + 5 类样例 Verifier 门禁）
eval-baseline: check-cgo
	@echo "Running F-26 Golden Sample Evaluation Baseline..."
	CGO_ENABLED=1 go test -v -count=1 ./pkg/codeir -run TestEval
	CGO_ENABLED=1 go test -v -count=1 ./pkg/verifier -run TestEvalGoldenSamplesVerifierBaseline
	@echo "✅ [Eval Baseline Passed] All F-04 / F-26 accuracy, security & golden sample assertions passed."

# 执行架构防腐检查与前端静态代码/类型校验
lint: lint-arch $(NODE_MODULES)
	cd $(FRONTEND_DIR) && npm run lint
