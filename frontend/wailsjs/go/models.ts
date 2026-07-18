export namespace config {
	
	export class SourceConfig {
	    provider: string;
	    api_key: string;
	    user_agent: string;
	    base_url?: string;
	    secrets?: Record<string, string>;
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SourceConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.api_key = source["api_key"];
	        this.user_agent = source["user_agent"];
	        this.base_url = source["base_url"];
	        this.secrets = source["secrets"];
	        this.enabled = source["enabled"];
	    }
	}
	export class Config {
	    library_roots: string[];
	    port: number;
	    nhentai_api_key: string;
	    nhentai_user_agent: string;
	    sources: SourceConfig[];
	    active_source: string;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.library_roots = source["library_roots"];
	        this.port = source["port"];
	        this.nhentai_api_key = source["nhentai_api_key"];
	        this.nhentai_user_agent = source["nhentai_user_agent"];
	        this.sources = this.convertValues(source["sources"], SourceConfig);
	        this.active_source = source["active_source"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class AutoTagOptions {
	    resync: boolean;
	    language_mode: string;
	
	    static createFrom(source: any = {}) {
	        return new AutoTagOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.resync = source["resync"];
	        this.language_mode = source["language_mode"];
	    }
	}
	export class MangaDetail {
	    manga: search.Manga;
	    pages: string[];
	    tags: tag.Typed[];
	    missing: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MangaDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.manga = this.convertValues(source["manga"], search.Manga);
	        this.pages = source["pages"];
	        this.tags = this.convertValues(source["tags"], tag.Typed);
	        this.missing = source["missing"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SourceCandidate {
	    gallery_id: string;
	    media_id: string;
	    thumbnail: string;
	    gallery_url: string;
	    english_title: string;
	    japanese_title: string;
	    num_pages: number;
	    num_favorites: number;
	    score: number;
	    title_score: number;
	    pages_exact: boolean;
	    page_delta: number;
	    language: string;
	    lang_match: boolean;
	    lang_mismatch: boolean;
	    artist_match: boolean;
	    parody_match: boolean;
	    tags: tag.Typed[];
	
	    static createFrom(source: any = {}) {
	        return new SourceCandidate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.gallery_id = source["gallery_id"];
	        this.media_id = source["media_id"];
	        this.thumbnail = source["thumbnail"];
	        this.gallery_url = source["gallery_url"];
	        this.english_title = source["english_title"];
	        this.japanese_title = source["japanese_title"];
	        this.num_pages = source["num_pages"];
	        this.num_favorites = source["num_favorites"];
	        this.score = source["score"];
	        this.title_score = source["title_score"];
	        this.pages_exact = source["pages_exact"];
	        this.page_delta = source["page_delta"];
	        this.language = source["language"];
	        this.lang_match = source["lang_match"];
	        this.lang_mismatch = source["lang_mismatch"];
	        this.artist_match = source["artist_match"];
	        this.parody_match = source["parody_match"];
	        this.tags = this.convertValues(source["tags"], tag.Typed);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class MatchResult {
	    manga_id: number;
	    local_title: string;
	    local_author: string;
	    local_pages: number;
	    local_language: string;
	    local_tags: tag.Typed[];
	    folder_path: string;
	    cover_rel_path?: string;
	    decision: string;
	    merge_gallery_ids: string[];
	    candidates: SourceCandidate[];
	
	    static createFrom(source: any = {}) {
	        return new MatchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.manga_id = source["manga_id"];
	        this.local_title = source["local_title"];
	        this.local_author = source["local_author"];
	        this.local_pages = source["local_pages"];
	        this.local_language = source["local_language"];
	        this.local_tags = this.convertValues(source["local_tags"], tag.Typed);
	        this.folder_path = source["folder_path"];
	        this.cover_rel_path = source["cover_rel_path"];
	        this.decision = source["decision"];
	        this.merge_gallery_ids = source["merge_gallery_ids"];
	        this.candidates = this.convertValues(source["candidates"], SourceCandidate);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SearchArgs {
	    q: string;
	    author_id: number;
	    tags: string[];
	    sort: string;
	    seed: number;
	    limit: number;
	    offset: number;
	
	    static createFrom(source: any = {}) {
	        return new SearchArgs(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.q = source["q"];
	        this.author_id = source["author_id"];
	        this.tags = source["tags"];
	        this.sort = source["sort"];
	        this.seed = source["seed"];
	        this.limit = source["limit"];
	        this.offset = source["offset"];
	    }
	}
	export class Settings {
	    has_nhentai_key: boolean;
	    nhentai_user_agent: string;
	    active_source: string;
	    active_source_label: string;
	    active_source_ready: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.has_nhentai_key = source["has_nhentai_key"];
	        this.nhentai_user_agent = source["nhentai_user_agent"];
	        this.active_source = source["active_source"];
	        this.active_source_label = source["active_source_label"];
	        this.active_source_ready = source["active_source_ready"];
	    }
	}
	
	export class SourceState {
	    slug: string;
	    label: string;
	    needs_key: boolean;
	    id_only: boolean;
	    has_key: boolean;
	    enabled: boolean;
	    active: boolean;
	    user_agent: string;
	
	    static createFrom(source: any = {}) {
	        return new SourceState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slug = source["slug"];
	        this.label = source["label"];
	        this.needs_key = source["needs_key"];
	        this.id_only = source["id_only"];
	        this.has_key = source["has_key"];
	        this.enabled = source["enabled"];
	        this.active = source["active"];
	        this.user_agent = source["user_agent"];
	    }
	}
	export class StashInput {
	    kind: string;
	    hash: string;
	    label: string;
	    manga_id: number;
	    page: number;
	
	    static createFrom(source: any = {}) {
	        return new StashInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.hash = source["hash"];
	        this.label = source["label"];
	        this.manga_id = source["manga_id"];
	        this.page = source["page"];
	    }
	}
	export class UnimportedPreview {
	    folder: scanner.DetectedFolder;
	    title: string;
	    author: string;
	    tags: tag.Typed[];
	
	    static createFrom(source: any = {}) {
	        return new UnimportedPreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.folder = this.convertValues(source["folder"], scanner.DetectedFolder);
	        this.title = source["title"];
	        this.author = source["author"];
	        this.tags = this.convertValues(source["tags"], tag.Typed);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace scanner {
	
	export class DetectedFolder {
	    folder_path: string;
	    author: string;
	    title: string;
	    page_count: number;
	    cover_rel_path?: string;
	
	    static createFrom(source: any = {}) {
	        return new DetectedFolder(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.folder_path = source["folder_path"];
	        this.author = source["author"];
	        this.title = source["title"];
	        this.page_count = source["page_count"];
	        this.cover_rel_path = source["cover_rel_path"];
	    }
	}

}

export namespace search {
	
	export class Author {
	    id: number;
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new Author(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	    }
	}
	export class Manga {
	    id: number;
	    title: string;
	    author_id: number;
	    folder_path: string;
	    cover_rel_path?: string;
	    page_count: number;
	    date_added: string;
	    date_modified: string;
	    missing: boolean;
	    nhentai_gallery_id?: number;
	    display_title?: string;
	    source_slug?: string;
	    source_ref?: string;
	    author_name: string;
	
	    static createFrom(source: any = {}) {
	        return new Manga(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.author_id = source["author_id"];
	        this.folder_path = source["folder_path"];
	        this.cover_rel_path = source["cover_rel_path"];
	        this.page_count = source["page_count"];
	        this.date_added = source["date_added"];
	        this.date_modified = source["date_modified"];
	        this.missing = source["missing"];
	        this.nhentai_gallery_id = source["nhentai_gallery_id"];
	        this.display_title = source["display_title"];
	        this.source_slug = source["source_slug"];
	        this.source_ref = source["source_ref"];
	        this.author_name = source["author_name"];
	    }
	}

}

export namespace stash {
	
	export class Entry {
	    id: number;
	    kind: string;
	    hash: string;
	    label: string;
	    last_page: number;
	    date_added: string;
	    manga_id?: number;
	    title: string;
	    author_name: string;
	    folder_path: string;
	    cover_rel_path?: string;
	
	    static createFrom(source: any = {}) {
	        return new Entry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.hash = source["hash"];
	        this.label = source["label"];
	        this.last_page = source["last_page"];
	        this.date_added = source["date_added"];
	        this.manga_id = source["manga_id"];
	        this.title = source["title"];
	        this.author_name = source["author_name"];
	        this.folder_path = source["folder_path"];
	        this.cover_rel_path = source["cover_rel_path"];
	    }
	}

}

export namespace tag {
	
	export class Typed {
	    name: string;
	    type: string;
	
	    static createFrom(source: any = {}) {
	        return new Typed(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.type = source["type"];
	    }
	}

}

