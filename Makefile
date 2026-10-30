.PHONY: smoke
smoke:
	cd services/runtime-adapter && pnpm install && pnpm smoke

.PHONY: worker-build phase2-demo phase2-eval

worker-build:
	docker build -t forge-worker:dev -f infrastructure/docker/worker/Dockerfile .

phase2-demo:
	cd services/runtime-adapter && pnpm forge run \
		--repo $(CURDIR)/tests/evaluations/fixtures/pagination-off-by-one \
		--task "Fix the off-by-one bug in src/paginate.ts. Run the tests to confirm."

phase2-eval:
	# Phase 6 formalizes this; for now, phase2-demo IS the first eval case.
	$(MAKE) phase2-demo

.PHONY: dev-mongo dev-control-plane dev-adapter phase3-demo phase3-resume-demo

dev-mongo:
	docker compose -f infrastructure/compose/mongo.yaml up -d

dev-control-plane:
	cd services/control-plane && go run ./cmd/forge

dev-adapter:
	cd services/runtime-adapter && pnpm dev

phase3-demo:
	bash scripts/phase3-demo.sh

phase3-resume-demo:
	bash scripts/phase3-resume-demo.sh

.PHONY: phase4-demo phase4-blocked-demo

phase4-demo:
	bash scripts/phase4-demo.sh

phase4-blocked-demo:
	bash scripts/phase4-blocked-demo.sh

phase5-demo:
	bash scripts/phase5-demo.sh

.PHONY: phase8-vertical-slice-demo

phase8-vertical-slice-demo:
	bash scripts/phase8-vertical-slice-demo.sh

.PHONY: eval console-install console-dev

eval:
	cd services/control-plane && go run ./cmd/eval

console-install:
	cd apps/console && pnpm install

console-dev:
	cd apps/console && pnpm dev

# RUN IT
# # 1. Build the worker image
# make worker-build

# # 2. Initialize the fixture repo (once)
# cd tests/evaluations/fixtures/pagination-off-by-one
# git init && git add . && git commit -m "initial"
# cd -

# # 3. Run the loop
# make phase2-demo

# EXPECTED_OUTPUT="Fix the off-by-one bug in src/paginate.ts. Run the tests to confirm."
# workspace forge-worker-xxxxxxxx at /tmp/forge-wt-xxxxx

# status: complete
# steps:  N
# final:  Fixed the off-by-one in src/paginate.ts by changing `end = start + size - 1` to `end = start + size`. All three tests pass.

# === diff ===
# diff --git a/src/paginate.ts b/src/paginate.ts
# ...
# -  const end = start + size - 1;
# +  const end = start + size;
# ...