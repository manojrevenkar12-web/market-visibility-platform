FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /cvp ./cmd/cvp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /cvp /cvp
EXPOSE 8080
ENTRYPOINT ["/cvp"]
