# 阶段1：编译
FROM golang:1.20-alpine AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o proxy main.go

# 阶段2：运行
FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/proxy .

# 暴露 10000 端口
ENV PORT=10000
EXPOSE 10000

CMD ["./proxy"]
