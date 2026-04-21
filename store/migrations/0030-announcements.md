Migration announcements are now delivered on startup.

The root group receives a system message listing pending announcements;
it is your job to fan out each announcement to every known chat, then
record delivery via `record_announcement_sent`.
