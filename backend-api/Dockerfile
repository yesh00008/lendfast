FROM node:18-alpine

WORKDIR /app

COPY package.json ./
RUN npm install --production

COPY index.js ./

EXPOSE 5001

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD wget -qO- http://localhost:5001/health || exit 1

CMD ["node", "index.js"]
