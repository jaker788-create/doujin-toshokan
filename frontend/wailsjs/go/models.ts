export namespace config {
	
	export class Config {
	    library_roots: string[];
	    port: number;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.library_roots = source["library_roots"];
	        this.port = source["port"];
	    }
	}

}

export namespace main {
	
	export class MangaDetail {
	    manga: search.Manga;
	    pages: string[];
	    tags: string[];
	    missing: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MangaDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.manga = this.convertValues(source["manga"], search.Manga);
	        this.pages = source["pages"];
	        this.tags = source["tags"];
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
	export class SearchArgs {
	    q: string;
	    author_id: number;
	    tags: string[];
	    sort: string;
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
	        this.limit = source["limit"];
	        this.offset = source["offset"];
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
	        this.author_name = source["author_name"];
	    }
	}

}

