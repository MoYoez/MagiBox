# --- 构建 ---
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /magibox ./cmd/bot

# --- 运行 ---
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /magibox /usr/local/bin/magibox
WORKDIR /data
VOLUME /data
EXPOSE 8099
ENTRYPOINT ["magibox"]
