FROM node:22-alpine AS build
WORKDIR /src
COPY whapd/package.json whapd/package-lock.json* ./
RUN npm install
COPY whapd/tsconfig.json ./
COPY whapd/src/ src/
RUN npx tsc

FROM node:22-alpine
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /src/package.json /src/package-lock.json* ./
COPY --from=build /src/node_modules node_modules/
COPY --from=build /src/dist dist/
ENTRYPOINT ["node", "dist/main.js"]
