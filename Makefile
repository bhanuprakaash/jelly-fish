.PHONY: infra down db deploy serve test chaos lint fmt

GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
IMAGE := localhost:5001/jelly-fish:latest

infra:
	@grep -q '^JF_MASTER_KEY=' deploy/k8s/.env.app 2>/dev/null || printf '\nJF_MASTER_KEY=m1:%s\n' "$$(openssl rand -base64 32)" >> deploy/k8s/.env.app
	kubectl apply -k deploy/k8s

# down removes the workloads and keeps the namespace, secrets and volumes, so
# make infra brings the same data back. The context is pinned: the current one
# may be a remote cluster.
down:
	kubectl --context jelly-fish -n jelly-fish delete deploy,statefulset,svc --all

db:
	kubectl port-forward -n jelly-fish svc/postgres 5433:5432

deploy:
	docker build --build-arg TAGS=dev -t $(IMAGE) .
	docker push $(IMAGE)
	kubectl apply -k deploy/k8s

serve:
	kubectl port-forward -n jelly-fish svc/api 8080:8080 & \
	kubectl port-forward -n jelly-fish svc/postgres 5433:5432 & \
	kubectl port-forward -n jelly-fish svc/mailpit 8025:8025 & \
	kubectl port-forward -n jelly-fish svc/jaeger 16686:16686 & \
	tailscale serve --bg --https=443 localhost:8080

test:
	go test -race ./...

chaos:
	go test -tags chaos -count=1 -timeout 5m ./internal/chaos/...

lint:
	$(GOLANGCI_LINT) run

fmt:
	$(GOLANGCI_LINT) fmt
