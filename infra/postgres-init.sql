-- Executed only for a fresh project-owned volume. psql quotes password values.
\getenv app_password WEBCHAT_DB_PASSWORD
\getenv temporal_password WEBCHAT_TEMPORAL_DB_PASSWORD
CREATE ROLE webchat LOGIN PASSWORD :'app_password';
CREATE ROLE temporal LOGIN PASSWORD :'temporal_password';
CREATE DATABASE webchat OWNER webchat;
CREATE DATABASE temporal OWNER temporal;
CREATE DATABASE temporal_visibility OWNER temporal;
REVOKE ALL ON DATABASE webchat FROM PUBLIC;
REVOKE ALL ON DATABASE temporal FROM PUBLIC;
REVOKE ALL ON DATABASE temporal_visibility FROM PUBLIC;
