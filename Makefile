.PHONY: infra down db deploy serve test chaos lint fmt

GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
IMAGE := localhost:5001/jelly-fish:latest
# The context is pinned: the current one may be a remote cluster.
KUBECTL := kubectl --context jelly-fish

infra:
	@grep -q '^JF_MASTER_KEY=' deploy/k8s/.env.app 2>/dev/null || printf '\nJF_MASTER_KEY=m1:%s\n' "$$(openssl rand -base64 32)" >> deploy/k8s/.env.app
	$(KUBECTL) apply -k deploy/k8s

# down removes the workloads and keeps the namespace, secrets and volumes, so
# make infra brings the same data back.
down:
	$(KUBECTL) -n jelly-fish delete deploy,statefulset,svc --all

db:
	$(KUBECTL) port-forward -n jelly-fish svc/postgres 5433:5432

deploy:
	docker build --build-arg TAGS=dev -t $(IMAGE) .
	docker push $(IMAGE)
	$(KUBECTL) apply -k deploy/k8s

serve:
	$(KUBECTL) port-forward -n jelly-fish svc/api 8080:8080 & \
	$(KUBECTL) port-forward -n jelly-fish svc/postgres 5433:5432 & \
	$(KUBECTL) port-forward -n jelly-fish svc/mailpit 8025:8025 & \
	$(KUBECTL) port-forward -n jelly-fish svc/jaeger 16686:16686 & \
	tailscale serve --bg --https=443 localhost:8080

test:
	go test -race ./...

chaos:
	go test -tags chaos -count=1 -timeout 5m ./internal/chaos/...

lint:
	$(GOLANGCI_LINT) run

fmt:
	$(GOLANGCI_LINT) fmt
