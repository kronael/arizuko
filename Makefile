image := kanipi
agent_image := kanipi-agent

image:
	docker build -t $(image) .

agent-image:
	./container/build.sh $(agent_image)

clean:
	rm -rf tmp/ dist/

.PHONY: image agent-image clean
