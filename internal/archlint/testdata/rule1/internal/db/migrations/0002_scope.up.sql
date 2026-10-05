ALTER TABLE destinations ADD COLUMN org_id bigint NOT NULL REFERENCES organizations (id);

CREATE TABLE events_2027 (LIKE events);
ALTER TABLE events ATTACH PARTITION events_2027 FOR VALUES FROM ('2027-01-01') TO ('2028-01-01');

CREATE TABLE archived_alerts (LIKE alerts INCLUDING ALL);

ALTER TABLE legacy DROP COLUMN org_id;

ALTER TABLE drafts RENAME TO drafts_v2;
