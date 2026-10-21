-- Offerings have no goods/services classification. Dropping the column also
-- removes its foreign key and the indexes that used it; other offering data
-- and the voucher relationship remain intact.
ALTER TABLE offerings DROP COLUMN commodity_type;

DROP TABLE commodity_type;
