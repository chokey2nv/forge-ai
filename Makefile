.PHONY: smoke
smoke:
	cd services/runtime-adapter && pnpm install && pnpm smoke

.PHONY: worker-build phase2-demo phase2-eval

worker-build:
	docker build -t forge-worker:dev --label forge.dockerfile=$$(shasum infrastructure/docker/worker/Dockerfile | cut -d' ' -f1) -f infrastructure/docker/worker/Dockerfile .

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

.PHONY: phase8-vertical-slice-demo phase8-two-child-slice phase8-failure-paths

phase8-vertical-slice-demo:
	bash scripts/phase8-vertical-slice-demo.sh

# Two dependent children integrating onto one parent branch.
phase8-two-child-slice:
	bash scripts/phase8-two-child-slice.sh

# Review, retry, exhaustion, crash/resume and add-an-agent paths with the scripted model (no API calls).
phase8-failure-paths:
	bash scripts/phase8-failure-paths.sh

.PHONY: phase10-parallel-slice

# Two independent subtasks in parallel plus one depending on both (real model).
phase10-parallel-slice:
	bash scripts/phase10-parallel-slice.sh

.PHONY: phase11-delivery-slice

# Approve -> merged into main locally, then rolled back (real model).
phase11-delivery-slice:
	bash scripts/phase11-delivery-slice.sh

.PHONY: phase12-feedback-slice

# A line comment, request changes, the agent fixes it and replies, approve (real model).
phase12-feedback-slice:
	bash scripts/phase12-feedback-slice.sh

.PHONY: eval console-install console-dev

# make eval [CASE=0002]; needs Forge running, and spends real money with a paid model.
eval:
	@bash scripts/forge.sh eval $(CASE)

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
.PHONY: up down user users docs-check

# Fails if the docs name files that are gone, or a code folder has no README.
docs-check:
	@bash scripts/check-docs.sh

# The whole of Forge in one command (scripts/forge.sh; RUNBOOK §4).
up:
	bash scripts/forge.sh up

down:
	bash scripts/forge.sh down

# make user NAME=ceejay [ROLE=viewer|member|reviewer] [ADMIN=1]; asks for the password.
user:
	@bash scripts/forge.sh user "$(NAME)" $(if $(ROLE),--role $(ROLE)) $(if $(ADMIN),--admin)

users:
	@bash scripts/forge.sh users
