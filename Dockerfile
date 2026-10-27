FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /fraudlens ./cmd/fraudlens

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /fraudlens /fraudlens
ENV FRAUD_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/fraudlens"]
