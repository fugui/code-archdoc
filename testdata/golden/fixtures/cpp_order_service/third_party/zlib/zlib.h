// Third-party zlib stub (must be skipped by Layer 1 noise filter)
#ifndef ZLIB_H
#define ZLIB_H
struct z_stream_s { int avail_in; };
int inflateInit(struct z_stream_s* strm);
#endif
