FROM golang:1.21 AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN go build -o /out/es-tmnt ./cmd/es-tmnt

FROM gcr.io/distroless/base-debian12
COPY --from=build /out/es-tmnt /usr/local/bin/es-tmnt
ENV ES_TMNT_HTTP_PORT=8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/es-tmnt"]
