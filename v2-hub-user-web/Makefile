.PHONY: run test build lint

node_modules: package-lock.json
	npm ci
	@touch node_modules

run: node_modules
	npm run dev

test: node_modules
	npm run type-check
	npm test

build: node_modules
	npm run build

lint: node_modules
	npm run lint
