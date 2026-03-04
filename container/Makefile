.PHONY: image

NAME ?=
IMAGE = kanipi-agent$(if $(NAME),-$(NAME))

image:
	docker build -t $(IMAGE) .
