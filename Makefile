.PHONY: test release-check

test: ## script regression tests and CHANGELOG format; no Go module exists yet
	python3 -m unittest discover -s scripts -p 'test_*.py'
	python3 scripts/changelog.py check

release-check: ## VERSION=vX.Y.Z: validate the CHANGELOG section before tagging
	@test -n "$(VERSION)" || { echo "usage: make release-check VERSION=vX.Y.Z"; exit 2; }
	python3 scripts/changelog.py check
	test "$$(python3 scripts/changelog.py latest)" = "$(VERSION)"
