VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/xlsft/openclick/internal/buildinfo.Version=$(VERSION) \
           -X github.com/xlsft/openclick/internal/buildinfo.Commit=$(COMMIT)

.PHONY: build test vet cross whisper build-whisper test-whisper whisper-clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

test:
	go test ./...

vet:
	go vet ./...

# Pure-Go packages must cross-compile for every target OS (ADR-0001 rule 6).
cross:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build ./...
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build ./...

# --- whisper.cpp (OC-29) -----------------------------------------------------
# Static whisper.cpp/ggml libraries for the `whisper` build tag, see
# docs/stt-protocol.md#whispercpp-build. GPU/BLAS backends are enabled only
# when the build host has them.

WHISPER_SRC     := third_party/whisper.cpp
WHISPER_BUILD   := third_party/whisper-build
WHISPER_PREFIX  := third_party/whisper-install
# ON tunes ggml to the build host's CPU; use OFF for redistributable binaries.
WHISPER_NATIVE  ?= ON
WHISPER_GENERATOR := $(if $(shell command -v ninja 2>/dev/null),-G Ninja,)

has = $(shell command -v $(1) >/dev/null 2>&1 && echo yes)
pc  = $(shell pkg-config --exists $(1) 2>/dev/null && echo yes)

WHISPER_DETECTED :=
WHISPER_DETECTED_LDFLAGS :=
# The loader's .pc file ships without headers on some distros (Arch:
# vulkan-icd-loader vs vulkan-headers), so check for vulkan.h as well.
vk_headers = $(shell test -f "$$(pkg-config --variable=includedir vulkan 2>/dev/null)/vulkan/vulkan.h" && echo yes)
ifeq ($(call pc,vulkan)$(vk_headers)$(call has,glslc),yesyesyes)
  WHISPER_DETECTED += -DGGML_VULKAN=ON
  WHISPER_DETECTED_LDFLAGS += -lggml-vulkan -lvulkan
endif
ifeq ($(call has,nvcc),yes)
  WHISPER_DETECTED += -DGGML_CUDA=ON
  WHISPER_DETECTED_LDFLAGS += -lggml-cuda -lcudart -lcublas -lcublasLt -lcuda
endif
ifeq ($(call pc,openblas),yes)
  WHISPER_DETECTED += -DGGML_BLAS=ON -DGGML_BLAS_VENDOR=OpenBLAS
  WHISPER_DETECTED_LDFLAGS += -lggml-blas -lopenblas
endif

WHISPER_CMAKE_EXTRA ?= $(strip $(WHISPER_DETECTED))
WHISPER_CGO_LDFLAGS ?= $(strip $(WHISPER_DETECTED_LDFLAGS))

WHISPER_CMAKE_FLAGS := -DCMAKE_BUILD_TYPE=Release \
	-DBUILD_SHARED_LIBS=OFF \
	-DWHISPER_BUILD_EXAMPLES=OFF \
	-DWHISPER_BUILD_TESTS=OFF \
	-DWHISPER_BUILD_SERVER=OFF \
	-DGGML_NATIVE=$(WHISPER_NATIVE) \
	$(WHISPER_CMAKE_EXTRA)

whisper:
	@test -f $(WHISPER_SRC)/CMakeLists.txt || git submodule update --init $(WHISPER_SRC)
	@command -v cmake >/dev/null || { echo "cmake not found: install cmake to build whisper.cpp" >&2; exit 1; }
	@echo "whisper.cpp backends: $(if $(WHISPER_CMAKE_EXTRA),$(WHISPER_CMAKE_EXTRA),cpu only)"
	cmake -S $(WHISPER_SRC) -B $(WHISPER_BUILD) $(WHISPER_GENERATOR) $(WHISPER_CMAKE_FLAGS)
	cmake --build $(WHISPER_BUILD) --config Release -j
	cmake --install $(WHISPER_BUILD) --prefix $(WHISPER_PREFIX)
	@printf '%s\n' '$(WHISPER_CGO_LDFLAGS)' > $(WHISPER_PREFIX)/cgo_ldflags

# Detected GPU/BLAS archives are linked as a second group that repeats the
# core libraries, because cgo's own LDFLAGS group cannot be extended.
WHISPER_GO_ENV = CGO_ENABLED=1 CGO_LDFLAGS="$$(x=$$(cat $(WHISPER_PREFIX)/cgo_ldflags 2>/dev/null); \
	if [ -n "$$x" ]; then echo "-L$(CURDIR)/$(WHISPER_PREFIX)/lib -Wl,--start-group -lwhisper -lggml -lggml-base -lggml-cpu $$x -Wl,--end-group"; fi)"

build-whisper:
	@test -f $(WHISPER_PREFIX)/lib/libwhisper.a || { echo "run 'make whisper' first" >&2; exit 1; }
	$(WHISPER_GO_ENV) go build -tags whisper -ldflags "$(LDFLAGS)" -o bin/ ./cmd/stt-sidecar

# Model-dependent tests skip unless OPENCLICK_WHISPER_MODEL points at a
# ggml model (models are never committed).
test-whisper:
	@test -f $(WHISPER_PREFIX)/lib/libwhisper.a || { echo "run 'make whisper' first" >&2; exit 1; }
	$(WHISPER_GO_ENV) go test -tags whisper -v ./internal/stt/...

whisper-clean:
	rm -rf $(WHISPER_BUILD) $(WHISPER_PREFIX)
