.PHONY: infra db test lint fmt

GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0

infra:
	kubectl apply -k deploy/k8s

db:
	kubectl port-forward -n jelly-fish svc/postgres 5433:5432

test:
	go test ./...

lint:
	$(GOLANGCI_LINT) run

fmt:
	$(GOLANGCI_LINT) fmt
