FROM node:24.21.0-bookworm-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20
WORKDIR /parser
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts && npm cache clean --force
COPY limit-pdf.mjs parser.mjs ./
RUN node limit-pdf.mjs
USER 65534:65534
ENTRYPOINT ["node", "--max-old-space-size=256", "/parser/parser.mjs"]
