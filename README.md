Инструкция по развертыванию
Установите Docker и Docker-compose

Создайте файл docker-compose.yml:

yaml
Copy
version: '3'

services:
  tarantool:
    image: tarantool/tarantool:latest
    ports:
      - "3301:3301"
    environment:
      - TARANTOOL_USER_NAME=admin
      - TARANTOOL_USER_PASSWORD=password
    command: tarantool -e "box.cfg{listen = 3301}; box.schema.user.passwd('admin', 'password'); require('console').start()"
  
  vote-bot:
    build: .
    ports:
      - "8080:8080"
    depends_on:
      - tarantool
    environment:
      - TARAN
