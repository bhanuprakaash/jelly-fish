.PHONY: infra db deploy serve test lint fmt

GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
IMAGE := localhost:5001/jelly-fish:latest

infra:
	kubectl apply -k deploy/k8s

db:
	kubectl port-forward -n jelly-fish svc/postgres 5433:5432

deploy:
	docker build -t $(IMAGE) .
	docker push $(IMAGE)
	kubectl apply -k deploy/k8s

serve:
	kubectl port-forward -n jelly-fish svc/api 8080:8080 & \
	kubectl port-forward -n jelly-fish svc/postgres 5432:5432 & \
	tailscale serve --bg 443 localhost:8080

test:
	go test ./...

lint:
	$(GOLANGCI_LINT) run

fmt:
	$(GOLANGCI_LINT) fmt
