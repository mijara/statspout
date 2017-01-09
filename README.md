Statspout
=========

Service that retrieves stats from Docker Containers and sends them to some repository (a.k.a DB).

Supported Repositories:

- Stdout (for testing)
- MongoDB (using https://github.com/go-mgo/mgo)
- Prometheus (as a scapre source, using https://github.com/prometheus/client_golang)
- InfluxDB (using https://github.com/influxdata/influxdb/tree/master/client)
