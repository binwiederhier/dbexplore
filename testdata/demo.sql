-- Demo database for local testing and README screenshots: a small online shop with foreign keys,
-- a composite key, JSON, arrays, a view, a materialized view, a partitioned table and some
-- awkward identifiers. Load it with: psql "$DSN" -f testdata/demo.sql
set client_min_messages = warning;
drop schema if exists shop, analytics, "Weird Schema" cascade;
create schema shop;
create schema analytics;
create schema "Weird Schema";

create table shop.customer (
  id bigserial primary key,
  email text not null unique,
  name text not null,
  country char(2) not null,
  vip boolean not null default false,
  tags text[] not null default '{}',
  prefs jsonb,
  created_at timestamptz not null default now()
);

create table shop.product (
  sku text primary key,
  title text not null,
  price numeric(10,2) not null,
  stock int not null,
  attrs jsonb not null default '{}'
);

create table shop."order" (
  id bigserial primary key,
  customer_id bigint not null references shop.customer(id),
  status text not null check (status in ('new','paid','shipped','cancelled')),
  total numeric(12,2) not null,
  note text,
  placed_at timestamptz not null
);
create index order_customer_idx on shop."order"(customer_id);
create index order_placed_idx on shop."order"(placed_at desc);

create table shop.order_item (
  order_id bigint not null references shop."order"(id),
  line int not null,
  sku text not null references shop.product(sku),
  qty int not null,
  unit_price numeric(10,2) not null,
  primary key (order_id, line)
);

create table shop.shipment (
  id serial primary key,
  order_id bigint not null,
  line int not null,
  carrier text,
  tracking uuid default gen_random_uuid(),
  shipped_at timestamp,
  foreign key (order_id, line) references shop.order_item(order_id, line)
);

create table analytics.event (
  id bigint generated always as identity,
  at timestamptz not null,
  kind text not null,
  payload jsonb,
  primary key (id, at)
) partition by range (at);
create table analytics.event_2026_09 partition of analytics.event for values from ('2026-09-01') to ('2026-10-01');
create table analytics.event_2026_10 partition of analytics.event for values from ('2026-10-01') to ('2026-11-01');

-- Reserved words as names: a bare "user" is current_user, a bare "order" a syntax error
create table shop."user" (id serial primary key, name text not null, "order" int, "desc" text);

create table "Weird Schema"."Mixed Case" ("ID" int primary key, "has space" text, "quote""d" text, big bigint, ratio double precision, "when" interval, ip inet, raw bytea);

-- Data
insert into shop.customer (email, name, country, vip, tags, prefs, created_at)
select 'user' || g || '@example.com',
       (array['Ada','Grace','Linus','Ken','Barbara','Dennis','Margaret','Edsger','Donald','Frances'])[1 + g % 10] || ' ' ||
       (array['Lovelace','Hopper','Torvalds','Thompson','Liskov','Ritchie','Hamilton','Dijkstra','Knuth','Allen'])[1 + (g / 10) % 10],
       (array['US','DE','FR','GB','JP','BR','IN','CA'])[1 + g % 8],
       g % 17 = 0,
       case when g % 3 = 0 then array['newsletter'] when g % 5 = 0 then array['beta','newsletter'] else '{}' end,
       case when g % 4 = 0 then null else jsonb_build_object('theme', (array['dark','light'])[1 + g % 2], 'lang', (array['en','de','fr'])[1 + g % 3], 'notify', jsonb_build_object('email', g % 2 = 0, 'push', true)) end,
       timestamptz '2025-01-01' + (g * interval '7 hours')
from generate_series(1, 1200) g;

insert into shop.product (sku, title, price, stock, attrs)
select 'SKU-' || lpad(g::text, 4, '0'),
       (array['Mechanical keyboard','USB-C hub','4K monitor','Desk lamp','Laptop stand','Webcam','Noise-cancelling headphones','Ergonomic mouse','Standing desk','Cable organizer'])[1 + g % 10] || ' ' || (array['Mini','Pro','Max','Lite','Plus'])[1 + g % 5],
       round((9.99 + (g * 37 % 500))::numeric, 2),
       (g * 13) % 250,
       jsonb_build_object('color', (array['black','white','silver'])[1 + g % 3], 'weight_g', 100 + g * 7 % 2000)
from generate_series(1, 80) g;

insert into shop."order" (customer_id, status, total, note, placed_at)
select 1 + (g * 7919) % 1200,
       (array['new','paid','paid','shipped','shipped','shipped','cancelled'])[1 + g % 7],
       0,
       case when g % 11 = 0 then 'Please leave at the door' when g % 29 = 0 then E'Gift wrap\nwith a card' end,
       timestamptz '2026-01-01' + (g * interval '53 minutes')
from generate_series(1, 5000) g;

insert into shop.order_item (order_id, line, sku, qty, unit_price)
select o.id, l, 'SKU-' || lpad((1 + (o.id * 31 + l * 17) % 80)::text, 4, '0'), 1 + (o.id + l) % 3, 0
from shop."order" o, generate_series(1, 1 + (o.id % 4)::int) l;
update shop.order_item i set unit_price = p.price from shop.product p where p.sku = i.sku;
update shop."order" o set total = s.t from (select order_id, sum(qty * unit_price) t from shop.order_item group by 1) s where s.order_id = o.id;

insert into shop.shipment (order_id, line, carrier, shipped_at)
select i.order_id, i.line, (array['UPS','DHL','FedEx','USPS'])[1 + i.order_id % 4], o.placed_at::timestamp + interval '1 day'
from shop.order_item i join shop."order" o on o.id = i.order_id where o.status = 'shipped' and i.line = 1;

insert into analytics.event (at, kind, payload)
select timestamptz '2026-09-01' + g * interval '17 minutes', (array['page_view','add_to_cart','checkout','search'])[1 + g % 4],
       jsonb_build_object('path', '/p/' || (g % 80), 'session', md5(g::text))
from generate_series(1, 4000) g;

insert into "Weird Schema"."Mixed Case" values
  (1, 'hello world', 'it''s "quoted"', 9007199254740993, 3.14159, interval '1 day 2 hours', '10.0.0.1', '\xdeadbeef'),
  (2, null, E'tab\there', -42, 'NaN', null, '::1', null),
  (3, repeat('long text ', 60), '<script>alert(1)</script>', 0, 'Infinity', interval '3 months', null, '\x00');

insert into shop."user" (name, "order", "desc") values ('admin', 3, 'Administrator'), ('support', 1, 'Support desk'), ('readonly', 2, null);

create view shop.customer_summary as
select c.id, c.name, c.country, count(o.id) as orders, coalesce(sum(o.total), 0) as revenue, max(o.placed_at) as last_order
from shop.customer c left join shop."order" o on o.customer_id = c.id
group by c.id;

create materialized view analytics.daily_revenue as
select date_trunc('day', placed_at)::date as day, count(*) as orders, sum(total) as revenue
from shop."order" where status <> 'cancelled' group by 1 order by 1;

analyze;
