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

# 暴露端口，方便面板做映射 (如果你本地或服务器面板需要别的端口，在这里修改映射即可)
ENV PORT=8080
EXPOSE 8080

CMD ["./proxy"]
