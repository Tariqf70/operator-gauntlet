# Local entry points. Run `make setup` once.
SHELL := /bin/bash
ENV := source .env.local &&

.PHONY: setup test selftest oracle validate pilot report

setup:          ## install tools into ./bin, build, write .env.local and runner/models.txt
	scripts/setup-local.sh

test:           ## vet and unit tests
	go vet ./... && go test ./pkg/...

selftest:       ## score the WebApp reference operator (expect 7/7, about 1 minute)
	$(ENV) bin/gauntlet run --spec specs/webapp --operator testdata/webapp-reference \
	  --converge-timeout 40s --quiet-window 20s --writer-duration 15s --log selftest.log

oracle:         ## run the whole pilot pipeline with the oracle adapter (expect 7/7)
	$(ENV) scripts/oracle.sh

validate:       ## references and mutants (about 10 minutes)
	$(ENV) testdata/validate.sh

pilot:          ## the real pilot, using runner/models.txt (hours; resumable)
	$(ENV) runner/run-pilot.sh

report:         ## summary table of results/*.json
	bin/gauntlet report results/*.json
