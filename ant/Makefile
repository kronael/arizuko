.PHONY: image test sync-skills

image:
	docker build -t arizuko-ant .

# The whole suite. This named src/heartbeat.test.ts, a file that does not exist
# — the target exited 1 on every run, so nothing was testing ant from make.
test:
	bun test

# Refresh the vendored skills from kronael/tools. The allow-list and the reason
# each excluded skill is excluded live in the script.
sync-skills:
	./sync-skills.sh
