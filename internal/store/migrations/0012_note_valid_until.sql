-- A note may end: valid_until is the last day it holds (YYYY-MM-DD); NULL is durable.
ALTER TABLE notes ADD COLUMN valid_until TEXT;
