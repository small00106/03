# fareclear

城市轨道交通票务清分引擎（内部服务）的第一阶段：**库表 + 计价内核**，不含 HTTP 接口。

- Go 1.22，PostgreSQL，[pgx/v5](https://github.com/jackc/pgx) 手写 SQL，无 ORM。
- 金额以「分」（BIGINT）存储，里程以「米」（INTEGER）存储。
- 计价规则按生效日期版本化；行程落库时**快照**当时版本与金额，历史行程不会被后来的规则改写。
- 站点图为物理站邻接表：换乘站只有一条 `stations` 记录，多条线路经 `line_stations` 挂接，图上即零代价换乘点。

## 目录结构

```
fareclear/
├── fare/                       # 计价内核（零外部依赖，纯 Go）
│   ├── model.go                # 领域模型
│   ├── graph.go                # 邻接表 + Dijkstra 最短路
│   ├── pricing.go              # 里程分段累进 / 封顶 / 票种折扣
│   ├── versions.go             # 规则版本选择（按计价归属日）
│   └── engine.go               # Engine 组装 + 闸机事件配对
└── store/
    ├── migrations/
    │   ├── 0001_init.sql       # 全部库表
    │   └── 0002_seed.sql       # 演示线网 / 票种 / 两版规则
    └── store.go                # pgx 连接、迁移、加载图与规则、事件落库、行程计价落库
```

## 计价规则（初始版本 FARE-2024，2024-01-01 起）

里程分段累进，界点归前段：

| 分段 | 规则 |
|---|---|
| 0–4 km（含 4km） | 一口价 3 元 |
| 4–16 km | 每 4 km 加 1 元（向上取整） |
| 16 km 以上 | 每 6 km 加 1 元（向上取整） |
| 全程 | 封顶 10 元 |

票种：

- `SINGLE` 单程票：全价；
- `STUDENT` 学生票：五折（万分位 5000bp），半分半数向上取整；
- `SENIOR` 敬老卡：实付 0，**仍按原价写入 `full_fare_cents` 与 `trip_legs`**，作为对运营方清分的依据。

**同站进出**：里程 0、无路径区间，但按起步价 3 元收费（AFC 通行口径）。

**票种停用**：`active=FALSE` 的票种在 `InsertGateEvents` 的**进站方向**被拒绝
（`ErrTicketTypeInactive`），不得用退役票种开新行程；**出站方向始终放行**——
乘客在途期间票种被停用（如 23:50 进站、运营停用、次日 00:20 出站），
出站事件必须能写入、行程必须能闭合。计价本身不读 active，只认 fare_kind/discount_bp，
因此停用前的历史行程与跨越停用时点的在途行程不受影响。

版本选择口径：`travel_date = 进站时刻在业务时区（Asia/Shanghai）下的日历日`。
跨零点行程（23:50 进站、次日 00:20 出站）仍按进站当天生效的版本计价。

## 库表

- 基础数据：`operators`、`lines`、`stations`、`line_stations`、`segments`（邻接表区间里程，可按区间指定运营方，缺省继承线路）
- 票种规则：`ticket_types`、`fare_rule_versions`（生效区间 GiST 互斥约束）、`fare_bands`（分段表）
- 交易：`gate_events`（闸机原始事件，只追加）、`trips`（行程 + 计价快照）、`trip_legs`（行程经过的区间，清分拆分底稿）
- 迁移记录：`schema_migrations`

## 使用

```go
ctx := context.Background()
st, _ := store.Open(ctx, os.Getenv("DATABASE_URL"), mustLoc("Asia/Shanghai"))
_ = st.Migrate(ctx)

eng, _ := st.NewEngine(ctx)

_, _ = st.InsertGateEvents(ctx, []store.GateEventInput{
    {CardNo: "C001", TicketTypeID: singleID, Time: enterAt, Direction: fare.Enter, StationID: s1},
    {CardNo: "C001", TicketTypeID: singleID, Time: exitAt,  Direction: fare.Exit,  StationID: s7},
})

// 事务内：取未入账事件 → 配对 → 寻路 → 选版本 → 计价 → 写 trips / trip_legs
trips, openEnters, anomalies, err := st.ProcessCard(ctx, "C001", eng)
```

已被 `trips` 引用的事件不会重复计价（`ProcessCard` 只捞未引用事件并 `FOR UPDATE` 锁定）。
配对结果分三类：已成账行程 `trips`、在途未闭合进站 `openEnters`（23:55 进站、
次日出站的正常状态，**不算异常、不报错**，下次处理时跨天配对）、真正无法配对的
`anomalies`（无进站的出站、被新进站冲掉的旧进站），后者同时以
`errors.Is(err, store.ErrUnpairedEvents)` 返回。

## 测试

```sh
go test ./...
```

纯 Go 单测覆盖：

- 分段界点 0 / 4km±1m / 8km±1m / 12km±1m / 16km±1m / 22 / 28 / 34km 与封顶；
- 独立参考公式对 0–60km 全轴交叉验证；
- 学生五折、敬老免费但原价照落、折扣半分取整；
- **跨零点行程按进站日选版本**（2025-12-31 23:50 → 2026-01-01 00:20 仍用 FARE-2024）；
- 历史行程在新增版本后重复计价结果不变；
- 换乘站零代价换线、无向图、不连通/未知站错误；
- 多卡交错事件配对不串卡。

真实 PostgreSQL 的端到端测试（建表、迁移幂等、跨零点落库、票种金额、重复入账防护）由环境变量开关，无库时自动跳过：

```sh
FARECLEAR_TEST_DATABASE='postgres://user:pass@localhost:5432/fareclear?sslmode=disable' go test ./...
```
