-- fareclear 初始 schema
-- 约定：
--   * 金额一律以「分」存 BIGINT，避免浮点误差；
--   * 距离以「米」存 INTEGER；
--   * 邻接表 segments 每条记录代表一个物理区间，图加载时按无向边处理
--     （如需单向区间可后续加 direction 列）；
--   * 换乘站在 stations 中只建一条物理站记录，多条线路经 line_stations 挂接，
--     图上即天然形成零代价换乘点；
--   * 计价规则按日期区间版本化，行程落库时把当时版本 id 与金额快照写死，
--     以后新增规则版本不会改写历史行程。

CREATE EXTENSION IF NOT EXISTS btree_gist;

-- 运营方 ---------------------------------------------------------------
CREATE TABLE operators (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code       TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 线路 -----------------------------------------------------------------
CREATE TABLE lines (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    operator_id BIGINT NOT NULL REFERENCES operators(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 车站（物理站；换乘站只此一条）----------------------------------------
CREATE TABLE stations (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code       TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 线路 <-> 车站 挂接（同一物理站挂多条线即换乘站）----------------------
CREATE TABLE line_stations (
    line_id    BIGINT NOT NULL REFERENCES lines(id) ON DELETE CASCADE,
    station_id BIGINT NOT NULL REFERENCES stations(id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    PRIMARY KEY (line_id, station_id),
    UNIQUE (line_id, position)
);

-- 区间里程（邻接表）-----------------------------------------------------
-- operator_id 留空时继承线路运营方；区间若由另一方实际运营可单独指定，
-- 为后续按运营方清分留好粒度。
CREATE TABLE segments (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    line_id         BIGINT NOT NULL REFERENCES lines(id) ON DELETE CASCADE,
    from_station_id BIGINT NOT NULL REFERENCES stations(id),
    to_station_id   BIGINT NOT NULL REFERENCES stations(id),
    distance_m      INTEGER NOT NULL CHECK (distance_m > 0),
    operator_id     BIGINT REFERENCES operators(id),
    CHECK (from_station_id <> to_station_id),
    UNIQUE (line_id, from_station_id, to_station_id)
);
CREATE INDEX idx_segments_from ON segments(from_station_id);
CREATE INDEX idx_segments_to   ON segments(to_station_id);

-- 票种 -------------------------------------------------------------------
-- fare_kind: full 全价 / rate 折扣 / free 免费
-- discount_bp: 万分位折扣，5000 = 五折；free 票记 0，仍按原价落账。
CREATE TABLE ticket_types (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    fare_kind   TEXT NOT NULL CHECK (fare_kind IN ('full', 'rate', 'free')),
    discount_bp INTEGER NOT NULL DEFAULT 0 CHECK (discount_bp BETWEEN 0 AND 10000),
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    CHECK (fare_kind <> 'rate' OR discount_bp BETWEEN 1 AND 9999),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 计价规则版本（按生效日期，区间互不重叠）-------------------------------
CREATE TABLE fare_rule_versions (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code           TEXT NOT NULL UNIQUE,
    valid_from     DATE NOT NULL,
    valid_until    DATE,
    cap_fare_cents BIGINT NOT NULL CHECK (cap_fare_cents > 0),
    remark         TEXT NOT NULL DEFAULT '',
    CHECK (valid_until IS NULL OR valid_until > valid_from),
    -- 任意两版本生效区间不得重叠：[valid_from, valid_until)
    EXCLUDE USING GIST (daterange(valid_from, valid_until, '[)') WITH &&)
);

-- 分段累进表 -------------------------------------------------------------
-- 第一段以 base_fare_cents 一口价；其后各段按落入该段的里程 / step_distance_m
-- 向上取整计 step_fare_cents 的倍数，全程受版本封顶约束。
CREATE TABLE fare_bands (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    rule_version_id  BIGINT NOT NULL REFERENCES fare_rule_versions(id) ON DELETE CASCADE,
    ordinal          INTEGER NOT NULL CHECK (ordinal >= 1),
    from_distance_m  INTEGER NOT NULL CHECK (from_distance_m >= 0),
    to_distance_m    INTEGER CHECK (to_distance_m IS NULL OR to_distance_m > from_distance_m),
    base_fare_cents  BIGINT NOT NULL DEFAULT 0 CHECK (base_fare_cents >= 0),
    step_distance_m  INTEGER CHECK (step_distance_m IS NULL OR step_distance_m > 0),
    step_fare_cents  BIGINT NOT NULL DEFAULT 0 CHECK (step_fare_cents >= 0),
    UNIQUE (rule_version_id, ordinal)
);

-- 闸机进出站原始事件（AFC 落地，不改写）---------------------------------
CREATE TABLE gate_events (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    card_no        TEXT NOT NULL,
    ticket_type_id BIGINT NOT NULL REFERENCES ticket_types(id),
    event_time     TIMESTAMPTZ NOT NULL,
    direction      TEXT NOT NULL CHECK (direction IN ('enter', 'exit')),
    station_id     BIGINT NOT NULL REFERENCES stations(id),
    gate_code      TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (card_no, event_time, direction)
);
CREATE INDEX idx_gate_events_card_time ON gate_events(card_no, event_time);

-- 行程（进出配对 + 计价快照）--------------------------------------------
-- 与金额相关的列（rule_version_id / full_fare_cents / paid_fare_cents /
-- distance_m / path_segment_ids）均为行程生成时刻的快照，只追加不改写。
CREATE TABLE trips (
    id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    card_no              TEXT NOT NULL,
    ticket_type_id       BIGINT NOT NULL REFERENCES ticket_types(id),
    enter_event_id       BIGINT UNIQUE REFERENCES gate_events(id),
    exit_event_id        BIGINT UNIQUE REFERENCES gate_events(id),
    origin_station_id    BIGINT NOT NULL REFERENCES stations(id),
    destination_station_id BIGINT NOT NULL REFERENCES stations(id),
    enter_time           TIMESTAMPTZ NOT NULL,
    exit_time            TIMESTAMPTZ NOT NULL,
    -- 计价归属日：按进站时刻在业务时区折算的日期
    travel_date          DATE NOT NULL,
    distance_m           INTEGER NOT NULL CHECK (distance_m >= 0),
    path_segment_ids     BIGINT[] NOT NULL DEFAULT '{}',
    fare_rule_version_id BIGINT NOT NULL REFERENCES fare_rule_versions(id),
    full_fare_cents      BIGINT NOT NULL CHECK (full_fare_cents >= 0),
    paid_fare_cents      BIGINT NOT NULL CHECK (paid_fare_cents >= 0),
    status               TEXT NOT NULL DEFAULT 'priced'
                           CHECK (status IN ('priced', 'anomaly')),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (exit_time > enter_time),
    CHECK (paid_fare_cents <= full_fare_cents)
);
CREATE INDEX idx_trips_travel_date ON trips(travel_date);
CREATE INDEX idx_trips_card_time   ON trips(card_no, enter_time);

-- 行程经过的区间（清分底稿：免费票也按原价落账，凭此向运营方拆分）-------
CREATE TABLE trip_legs (
    trip_id     BIGINT NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    ordinal     INTEGER NOT NULL CHECK (ordinal >= 1),
    segment_id  BIGINT NOT NULL REFERENCES segments(id),
    line_id     BIGINT NOT NULL REFERENCES lines(id),
    operator_id BIGINT NOT NULL REFERENCES operators(id),
    distance_m  INTEGER NOT NULL CHECK (distance_m > 0),
    PRIMARY KEY (trip_id, ordinal)
);
CREATE INDEX idx_trip_legs_operator ON trip_legs(operator_id);
