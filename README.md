# Переговорные комнаты

Учебный проект системы бронирования переговорных комнат. 
## Запуск



```sh
docker compose up -d --build
```

 **http://localhost:8081**.

Учетки:

| Почта | Пароль для локального запуска |
| --- | --- |
| user@example.com | conference123 |
| colleague@example.com | conference123 |

Учётные записи создаются в миграции `migrations/001_init.sql`. В БД записываются готовые хеши паролей Argon2id.



## Настройки

Для своих значений скопировать `.env.example` в `.env` перед первым запуском:

```sh
cp .env.example .env
```

| Переменная | Назначение |
| --- | --- |
| DB_PASSWORD | Пароль PostgreSQL. Для строки подключения использовать буквы, цифры, `_`, `-` |
| HTTP_PORT | Порт на хосте, по умолчанию 8081 |


## API


| Метод | Путь | Данные или результат |
| --- | --- | --- |
| POST | /api/login | `{ "email": "...", "password": "..." }`, сессионная cookie |
| POST | /api/logout | `{}`, завершение сессии |
| GET | /api/me | Текущий сотрудник |
| GET | /api/rooms?starts_at=...&ends_at=... | Свободные комнаты, названия и вместимость |
| GET | /api/bookings | Только свои брони |
| POST | /api/bookings | `{ "room_id": 1, "starts_at": "...", "ends_at": "..." }` |
| POST | /api/bookings/{id}/cancel | `{}`, отменённая бронь |
| GET | /health | Проверка соединения с БД |

