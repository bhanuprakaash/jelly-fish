.PHONY: infra db

infra:
	kubectl apply -k deploy/k8s

db:
	kubectl port-forward -n jelly-fish svc/postgres 5433:5432
