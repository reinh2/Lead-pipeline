SHELL := /bin/bash
COMPOSE := docker compose

.PHONY: up down dev fmt vet test test-race check compose-config demo-lead demo-lead-hot logs

up:
	$(COMPOSE) up -d --build
	@echo "Landing: http://localhost:8080  Pipeline: http://localhost:8081/healthz"

down:
	$(COMPOSE) down

dev:
	cd pipeline && go run ./cmd/pipeline

fmt:
	cd pipeline && gofmt -w $$(find . -type f -name '*.go' -not -path './vendor/*')

vet:
	cd pipeline && go vet ./...

test:
	cd pipeline && go test ./...

test-race:
	cd pipeline && go test -race ./...

check:
	cd pipeline && test -z "$$(gofmt -d $$(find . -type f -name '*.go' -not -path './vendor/*'))"
	cd pipeline && go vet ./...
	cd pipeline && go test -race ./...

compose-config:
	$(COMPOSE) config --quiet

logs:
	$(COMPOSE) logs -f pipeline

demo-lead:
	@curl -sS -X POST http://localhost:8081/api/leads \
	  -H 'Content-Type: application/json' \
	  -d '{ \
	    "name":"Priya Sharma", \
	    "email":"priya.sharma@example.com", \
	    "nationality":"Indian", \
	    "current_country":"India", \
	    "service":"full_relocation", \
	    "move_date":"2026-10-01", \
	    "family_size":2, \
	    "message":"Interviewing at two Berlin startups, likely offer next month. Wife will join.", \
	    "gdpr_consent":true \
	  }' | tee /dev/stderr | jq .

demo-lead-hot:
	@curl -sS -X POST http://localhost:8081/api/leads \
	  -H 'Content-Type: application/json' \
	  -d '{ \
	    "name":"Alex Chen", \
	    "email":"alex.chen@example.com", \
	    "nationality":"Canadian", \
	    "current_country":"Canada", \
	    "service":"full_relocation", \
	    "move_date":"2026-09-01", \
	    "family_size":3, \
	    "message":"Signed offer with N26, start date Sep 1. Need visa help, apartment in Prenzlauer Berg, school for two kids.", \
	    "gdpr_consent":true \
	  }' | tee /dev/stderr | jq .
