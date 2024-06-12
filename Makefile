export $(shell sed 's/=.*//' .env)

server:
	@cd cmd/tracker && go build && ./tracker -ip 123.123.123.123

dashboard:
	@cd cmd/dashboard && \
	go build -o localdash && \
	./localdash -site 1 -start 20240602 -end 20240630
