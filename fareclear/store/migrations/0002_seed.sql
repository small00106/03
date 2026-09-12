-- 种子数据（演示线网 + 票种 + 两版计价规则）
-- 线网示意（S3 为 L1/L2 换乘物理站）：
--   L1(运营方甲):  S1 --4km-- S2 --4km-- S3
--   L2(运营方乙):  S3 --6km-- S4 --6km-- S5 --6km-- S6 --6km-- S7
-- 例如 S1->S7 = 32km：3 元 + 12km/4km*1 元 + 16km/6km 向上取整 3 元 = 9 元。

BEGIN;

INSERT INTO operators (code, name) VALUES
    ('OP-A', '甲运营公司'),
    ('OP-B', '乙运营公司');

INSERT INTO lines (code, name, operator_id) VALUES
    ('L1', '一号线', (SELECT id FROM operators WHERE code = 'OP-A')),
    ('L2', '二号线', (SELECT id FROM operators WHERE code = 'OP-B'));

INSERT INTO stations (code, name) VALUES
    ('S1', '西安里'),
    ('S2', '文化路'),
    ('S3', '中央门'),   -- 换乘站，只建一条物理站记录
    ('S4', '东湖'),
    ('S5', '云栖'),
    ('S6', '南桥'),
    ('S7', '新港');

INSERT INTO line_stations (line_id, station_id, position) VALUES
    ((SELECT id FROM lines WHERE code='L1'), (SELECT id FROM stations WHERE code='S1'), 1),
    ((SELECT id FROM lines WHERE code='L1'), (SELECT id FROM stations WHERE code='S2'), 2),
    ((SELECT id FROM lines WHERE code='L1'), (SELECT id FROM stations WHERE code='S3'), 3),
    ((SELECT id FROM lines WHERE code='L2'), (SELECT id FROM stations WHERE code='S3'), 1),
    ((SELECT id FROM lines WHERE code='L2'), (SELECT id FROM stations WHERE code='S4'), 2),
    ((SELECT id FROM lines WHERE code='L2'), (SELECT id FROM stations WHERE code='S5'), 3),
    ((SELECT id FROM lines WHERE code='L2'), (SELECT id FROM stations WHERE code='S6'), 4),
    ((SELECT id FROM lines WHERE code='L2'), (SELECT id FROM stations WHERE code='S7'), 5);

-- 邻接表：每个物理区间一行，图加载时按无向边展开。
INSERT INTO segments (line_id, from_station_id, to_station_id, distance_m, operator_id) VALUES
    ((SELECT id FROM lines WHERE code='L1'), (SELECT id FROM stations WHERE code='S1'), (SELECT id FROM stations WHERE code='S2'), 4000, (SELECT id FROM operators WHERE code='OP-A')),
    ((SELECT id FROM lines WHERE code='L1'), (SELECT id FROM stations WHERE code='S2'), (SELECT id FROM stations WHERE code='S3'), 4000, (SELECT id FROM operators WHERE code='OP-A')),
    ((SELECT id FROM lines WHERE code='L2'), (SELECT id FROM stations WHERE code='S3'), (SELECT id FROM stations WHERE code='S4'), 6000, (SELECT id FROM operators WHERE code='OP-B')),
    ((SELECT id FROM lines WHERE code='L2'), (SELECT id FROM stations WHERE code='S4'), (SELECT id FROM stations WHERE code='S5'), 6000, (SELECT id FROM operators WHERE code='OP-B')),
    ((SELECT id FROM lines WHERE code='L2'), (SELECT id FROM stations WHERE code='S5'), (SELECT id FROM stations WHERE code='S6'), 6000, (SELECT id FROM operators WHERE code='OP-B')),
    ((SELECT id FROM lines WHERE code='L2'), (SELECT id FROM stations WHERE code='S6'), (SELECT id FROM stations WHERE code='S7'), 6000, (SELECT id FROM operators WHERE code='OP-B'));

INSERT INTO ticket_types (code, name, fare_kind, discount_bp) VALUES
    ('SINGLE', '单程票',   'full', 0),
    ('STUDENT', '学生票',  'rate', 5000),
    ('SENIOR',  '敬老卡',  'free', 0);

-- 第一版规则：2024-01-01 起至 2026-01-01（不含）。
INSERT INTO fare_rule_versions (code, valid_from, valid_until, cap_fare_cents, remark) VALUES
    ('FARE-2024', DATE '2024-01-01', DATE '2026-01-01', 1000, '初始规则：4km内3元，4-16km每4km加1元，16km以上每6km加1元，封顶10元');

INSERT INTO fare_bands (rule_version_id, ordinal, from_distance_m, to_distance_m, base_fare_cents, step_distance_m, step_fare_cents) VALUES
    ((SELECT id FROM fare_rule_versions WHERE code='FARE-2024'), 1, 0,     4000,  300, NULL,   0),
    ((SELECT id FROM fare_rule_versions WHERE code='FARE-2024'), 2, 4000,  16000, 0,   4000,  100),
    ((SELECT id FROM fare_rule_versions WHERE code='FARE-2024'), 3, 16000, NULL,  0,   6000,  100);

-- 第二版规则：2026-01-01 起长期有效（起步价调整为 4 元、封顶 12 元），
-- 仅用于演示版本选择；2026 年之前的行程仍按 FARE-2024 计价。
INSERT INTO fare_rule_versions (code, valid_from, valid_until, cap_fare_cents, remark) VALUES
    ('FARE-2026', DATE '2026-01-01', NULL, 1200, '2026 调整：起步4元，4-16km每4km加1元，16km以上每6km加1元，封顶12元');

INSERT INTO fare_bands (rule_version_id, ordinal, from_distance_m, to_distance_m, base_fare_cents, step_distance_m, step_fare_cents) VALUES
    ((SELECT id FROM fare_rule_versions WHERE code='FARE-2026'), 1, 0,     4000,  400, NULL,   0),
    ((SELECT id FROM fare_rule_versions WHERE code='FARE-2026'), 2, 4000,  16000, 0,   4000,  100),
    ((SELECT id FROM fare_rule_versions WHERE code='FARE-2026'), 3, 16000, NULL,  0,   6000,  100);

COMMIT;
