# InfluxDB Relay

在官方版本的的基础上，新增以下功能

- 数据分片
- 支持query，并提供查询合并
- 语句检查

## Configuration
配置基本与官方相似

```toml
[[http]]
name = "influx-relay"
bind-addr = "0.0.0.0:8360"
replicas = 500
default-retention-policy = "autogen"

[http.output]

# 列表中每个节点存放相同的数据，以此作为冗余
a = [
        { name="influxdb1", location = "http://influxdb1:8086", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
        { name="influxdb2", location = "http://influxdb2:8086", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
        { name="kapacitor1", location = "http://kapacitor1:9092", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
    ]

b = [
        { name="influxdb1", location = "http://influxdb1:8086", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
        { name="influxdb2", location = "http://influxdb2:8086", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
        { name="kapacitor1", location = "http://kapacitor1:9092", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
    ]

c = [
        { name="influxdb1", location = "http://influxdb1:8086", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
        { name="influxdb2", location = "http://influxdb2:8086", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
        { name="kapacitor1", location = "http://kapacitor1:9092", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
    ]

# [http.former]用于扩容前的节点配置
[http.former]
a = [
        { name="influxdb1", location = "http://influxdb1:8086", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
        { name="influxdb2", location = "http://influxdb2:8086", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
        { name="kapacitor1", location = "http://kapacitor1:9092", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
    ]

b = [
        { name="influxdb1", location = "http://influxdb1:8086", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
        { name="influxdb2", location = "http://influxdb2:8086", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
        { name="kapacitor1", location = "http://kapacitor1:9092", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
    ]

c = [
        { name="influxdb1", location = "http://influxdb1:8086", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
        { name="influxdb2", location = "http://influxdb2:8086", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
        { name="kapacitor1", location = "http://kapacitor1:9092", buffer-size-mb = 8192, max-batch-kb = 500, max-delay-interval = "10s" },
    ]
```

## Description
relay提供query、write操作，取消了官方udp功能

![](http://ohjpfpjyb.bkt.clouddn.com/picture/2018-06-28-influxdb-relay.png)

## Shard
数据基于measurement使用一致性hash进行分片。每份数据可以存多份进行冗余。

## Expansion
扩容后可以在配置中同时设置扩容前、后的节点信息，query操作会对结果进行合并